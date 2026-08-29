package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"pellets/internal/domain"
)

func TestOpenMigratesRealFileAndConfiguresRuntime(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pellets.db")
	started := time.Now()
	db := openTestDatabase(t, path)
	finished := time.Now()
	defer db.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	assertPragmaInt(t, db, "foreign_keys", 1)
	assertPragmaInt(t, db, "trusted_schema", 0)
	assertPragmaInt(t, db, "synchronous", 2)
	assertPragmaInt(t, db, "busy_timeout", 5000)

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want WAL", journalMode)
	}

	var sourceID string
	if err := db.QueryRow("SELECT fts5_source_id()").Scan(&sourceID); err != nil {
		t.Fatalf("FTS5 capability check failed: %v", err)
	}
	if sourceID == "" {
		t.Fatal("FTS5 source ID is empty")
	}

	assertPragmaInt(t, db, "user_version", LatestSchemaVersion)

	assertSchemaObjects(t, db)
	assertStrictTables(t, db)
	assertPartialIndexes(t, db)
	assertFTSIndexes(t, db)
	identity := assertFreshIdentityMetadata(t, db, started, finished)

	// Reopening at the latest version applies no migration.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDatabase(t, path)
	defer db.Close()
	assertPragmaInt(t, db, "user_version", LatestSchemaVersion)
	assertApplicationMetadataEqual(t, readApplicationMetadata(t, db), identity)
}

func TestFreshDatabasesReceiveDistinctDatabaseIDs(t *testing.T) {
	t.Parallel()

	var databaseIDs [2]string
	for index := range databaseIDs {
		started := time.Now()
		db := openTestDatabase(t, filepath.Join(t.TempDir(), fmt.Sprintf("fresh-%d.db", index)))
		finished := time.Now()
		metadata := assertFreshIdentityMetadata(t, db, started, finished)
		databaseIDs[index] = metadata["database_id"]
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if databaseIDs[0] == databaseIDs[1] {
		t.Fatalf("fresh databases received the same database_id %q", databaseIDs[0])
	}
}

func TestMemoryIDsAreNeverReusedAfterRemoval(t *testing.T) {
	t.Parallel()

	db := openTestDatabase(t, filepath.Join(t.TempDir(), "memory-ids.db"))
	defer db.Close()

	mustInsertProjectAndWorkspace(t, db, 1, 1, "memory", "memory")

	addMemory := func(text string) int64 {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		var memoryID int64
		if err := tx.QueryRow(`
			INSERT INTO memories(project_id, text, created_by, created_at, updated_at)
			VALUES (1, ?, 'agent', 1, 1)
			RETURNING memory_id`, text).Scan(&memoryID); err != nil {
			t.Fatalf("insert memory: %v", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO memories_fts(rowid, text)
			VALUES (?, ?)`, memoryID, text); err != nil {
			t.Fatalf("index memory %d: %v", memoryID, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit memory %d: %v", memoryID, err)
		}
		return memoryID
	}

	removeMemory := func(memoryID int64, text string) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`
			INSERT INTO memories_fts(memories_fts, rowid, text)
			VALUES ('delete', ?, ?)`, memoryID, text); err != nil {
			t.Fatalf("remove memory %d from FTS: %v", memoryID, err)
		}
		result, err := tx.Exec("DELETE FROM memories WHERE memory_id = ?", memoryID)
		if err != nil {
			t.Fatalf("remove memory %d: %v", memoryID, err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			t.Fatalf("remove memory %d affected %d rows: %v", memoryID, affected, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit removal of memory %d: %v", memoryID, err)
		}
	}

	assertFTSRows := func(query string, want ...int64) {
		t.Helper()
		rows, err := db.Query(`
			SELECT rowid
			FROM memories_fts
			WHERE memories_fts MATCH ?
			ORDER BY rowid`, query)
		if err != nil {
			t.Fatalf("search memory FTS for %q: %v", query, err)
		}
		defer rows.Close()

		var got []int64
		for rows.Next() {
			var memoryID int64
			if err := rows.Scan(&memoryID); err != nil {
				t.Fatal(err)
			}
			got = append(got, memoryID)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("memory FTS rows for %q = %v, want %v", query, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("memory FTS rows for %q = %v, want %v", query, got, want)
			}
		}
	}

	firstText := "sharedanchor firstanchor"
	highestText := "sharedanchor highestanchor"
	firstID := addMemory(firstText)
	highestID := addMemory(highestText)
	removeMemory(highestID, highestText)

	replacementText := "sharedanchor replacementanchor"
	replacementID := addMemory(replacementText)
	if replacementID <= highestID {
		t.Fatalf("memory ID after removing highest committed ID = %d, want greater than %d", replacementID, highestID)
	}
	assertFTSRows("sharedanchor", firstID, replacementID)
	assertFTSRows("highestanchor")

	removeMemory(firstID, firstText)
	removeMemory(replacementID, replacementText)
	assertFTSRows("sharedanchor")

	afterEmptyID := addMemory("sharedanchor afteremptyanchor")
	if afterEmptyID <= replacementID {
		t.Fatalf("memory ID after deleting all memories = %d, want greater than previous maximum %d", afterEmptyID, replacementID)
	}
	assertFTSRows("sharedanchor", afterEmptyID)
	assertFTSRows("firstanchor")
	assertFTSRows("replacementanchor")

	var sequenceTables string
	if err := db.QueryRow(`
		SELECT group_concat(name, ',')
		FROM (SELECT name FROM sqlite_sequence ORDER BY name)`).Scan(&sequenceTables); err != nil {
		t.Fatal(err)
	}
	if sequenceTables != "memories" {
		t.Fatalf("AUTOINCREMENT sequence tables = %q, want only memories", sequenceTables)
	}

	var autoincrementTables string
	if err := db.QueryRow(`
		SELECT group_concat(name, ',')
		FROM (
			SELECT name
			FROM sqlite_schema
			WHERE type = 'table' AND instr(upper(sql), 'AUTOINCREMENT') > 0
			ORDER BY name
		)`).Scan(&autoincrementTables); err != nil {
		t.Fatal(err)
	}
	if autoincrementTables != "memories" {
		t.Fatalf("tables declared AUTOINCREMENT = %q, want only memories", autoincrementTables)
	}
}

func TestSchemaEnforcesForeignKeysAndPelletQueueConstraints(t *testing.T) {
	t.Parallel()

	db := openTestDatabase(t, filepath.Join(t.TempDir(), "constraints.db"))
	defer db.Close()

	assertExecFails(t, db, `
		INSERT INTO pellets(project_id, number, title, status, priority, created_at, updated_at)
		VALUES (999, 1, 'missing project', 'open', 1024, 1, 1)`)

	mustInsertProjectAndWorkspace(t, db, 1, 11, "one", "one-main")
	mustExec(t, db, `
		INSERT INTO project_workspaces(
			workspace_id, project_id, root_path, root_path_relative,
			git_dir, git_dir_relative, created_at, updated_at
		) VALUES (12, 1, 'one-linked', 1, 'one/.git/worktrees/linked', 1, 1, 1)`)
	mustInsertProjectAndWorkspace(t, db, 2, 21, "two", "two-main")
	mustInsertPellet(t, db, 1, 1, "open", nil, intPtr(1024), nil)

	// Priority stays project-scoped while ownership is workspace-scoped.
	assertInsertPelletFails(t, db, 1, 2, "open", nil, intPtr(1024), nil)
	assertInsertPelletFails(t, db, 1, 3, "in_progress", nil, intPtr(2048), nil)
	assertInsertPelletFails(t, db, 1, 4, "open", intPtr(11), intPtr(2048), nil)
	assertInsertPelletFails(t, db, 1, 5, "in_progress", intPtr(21), intPtr(2048), nil)
	mustInsertPellet(t, db, 1, 6, "in_progress", intPtr(11), intPtr(2048), nil)
	assertInsertPelletFails(t, db, 1, 7, "in_progress", intPtr(11), intPtr(3072), nil)
	mustInsertPellet(t, db, 1, 8, "in_progress", intPtr(12), intPtr(3072), nil)
	mustInsertPellet(t, db, 2, 1, "open", nil, intPtr(1024), nil)
	mustInsertPellet(t, db, 2, 2, "in_progress", intPtr(21), intPtr(2048), nil)

	assertInsertPelletFails(t, db, 1, 9, "closed", nil, intPtr(4096), floatPtr(2))
	assertInsertPelletFails(t, db, 1, 10, "maybe_later", nil, intPtr(4096), nil)
	assertInsertPelletFails(t, db, 1, 11, "open", nil, nil, nil)
	assertInsertPelletFails(t, db, 1, 12, "closed", nil, nil, nil)
	mustInsertPellet(t, db, 1, 13, "closed", nil, nil, floatPtr(2))
	mustInsertPellet(t, db, 1, 14, "maybe_later", nil, nil, nil)

	assertExecFails(t, db, "DELETE FROM projects WHERE project_id = 1")
	var violations int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if violations != 0 {
		t.Fatalf("foreign key violations = %d, want 0", violations)
	}
}

func TestUnsupportedSchemaVersionsAreRejectedWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name     string
		version  int
		wantCode string
	}{
		{name: "negative", version: -1, wantCode: "schema_version_invalid"},
		{name: "unsupported", version: 0, wantCode: "schema_version_unsupported"},
		{name: "newer", version: LatestSchemaVersion + 1, wantCode: "schema_too_new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), test.name+".db")
			raw := openRawDatabase(t, path)
			mustExec(t, raw, `
				CREATE TABLE marker (value TEXT NOT NULL) STRICT;
				INSERT INTO marker VALUES ('unchanged');`)
			mustExec(t, raw, fmt.Sprintf("PRAGMA user_version = %d", test.version))
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			db, err := Open(context.Background(), path)
			if db != nil {
				db.Close()
				t.Fatalf("Open returned a database for user_version %d", test.version)
			}
			assertDomainErrorCode(t, err, test.wantCode)
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(after, before) {
				t.Fatalf("database with user_version %d changed while being rejected", test.version)
			}
			for _, suffix := range []string{"-journal", "-wal", "-shm"} {
				if _, statErr := os.Stat(path + suffix); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("unexpected sidecar %q after rejection: %v", path+suffix, statErr)
				}
			}
		})
	}
}

func TestIncompatibleAndCorruptDatabasesAreRejectedWithoutWrites(t *testing.T) {
	t.Run("incompatible file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-sqlite.db")
		if err := os.WriteFile(path, []byte("this is not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertOpenFailureLeavesFileUnchanged(t, path, "database_incompatible")
	})

	t.Run("corrupt btree", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "corrupt.db")
		createCorruptDatabaseFixture(t, path)
		assertOpenFailureLeavesFileUnchanged(t, path, "database_corrupt")
	})
}

func TestSupportedVersionOnIncompatibleSchemaIsRejectedWithoutWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "wrong-current-schema.db")
	raw := openRawDatabase(t, path)
	mustExec(t, raw, fmt.Sprintf(`
		CREATE TABLE unrelated (value TEXT NOT NULL) STRICT;
		INSERT INTO unrelated VALUES ('unchanged');
		PRAGMA user_version = %d;`, LatestSchemaVersion))
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	assertOpenFailureLeavesFileUnchanged(t, path, "schema_version_unsupported")
}

func TestStructurallyMismatchedSupportedSchemasAreRejectedWithoutWrites(t *testing.T) {
	for version := 1; version <= LatestSchemaVersion; version++ {
		version := version
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), fmt.Sprintf("partial-version-%d.db", version))
			raw := openRawDatabase(t, path)
			mustExec(t, raw, previouslyAcceptedPartialSchema(version))
			mustExec(t, raw, fmt.Sprintf("PRAGMA user_version = %d", version))
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			assertOpenFailureLeavesFileUnchanged(t, path, "schema_version_unsupported")
		})
	}
}

func TestCurrentSchemaContractRejectsStructuralDriftWithoutWrites(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{
			name: "column type and nullability",
			mutate: func(t *testing.T, database *sql.DB) {
				recreateApplicationMetadata(t, database, `
					CREATE TABLE application_metadata (
						key   TEXT PRIMARY KEY,
						value TEXT
					) STRICT;`)
			},
		},
		{
			name: "primary key",
			mutate: func(t *testing.T, database *sql.DB) {
				recreateApplicationMetadata(t, database, `
					CREATE TABLE application_metadata (
						key   TEXT NOT NULL,
						value TEXT NOT NULL
					) STRICT;`)
			},
		},
		{
			name: "check constraint",
			mutate: func(t *testing.T, database *sql.DB) {
				rewriteSchemaObject(t, database, "memories", "CHECK (trim(text) <> '')", "CHECK (length(text) >= 0)")
			},
		},
		{
			name: "foreign key action",
			mutate: func(t *testing.T, database *sql.DB) {
				rewriteSchemaObject(t, database, "project_workspaces", "ON DELETE RESTRICT", "ON DELETE CASCADE")
			},
		},
		{
			name: "strict mode",
			mutate: func(t *testing.T, database *sql.DB) {
				recreateApplicationMetadata(t, database, `
					CREATE TABLE application_metadata (
						key   TEXT PRIMARY KEY,
						value TEXT NOT NULL
					);`)
			},
		},
		{
			name: "required index",
			mutate: func(t *testing.T, database *sql.DB) {
				mustExec(t, database, "DROP INDEX pellets_closed_completed_idx")
			},
		},
		{
			name: "index uniqueness",
			mutate: func(t *testing.T, database *sql.DB) {
				mustExec(t, database, `
					DROP INDEX pellets_workspace_in_progress_idx;
					CREATE INDEX pellets_workspace_in_progress_idx
					ON pellets(workspace_id) WHERE status = 'in_progress';`)
			},
		},
		{
			name: "FTS configuration",
			mutate: func(t *testing.T, database *sql.DB) {
				mustExec(t, database, `
					DROP TABLE pellets_fts;
					CREATE VIRTUAL TABLE pellets_fts USING fts5(
						title,
						description,
						external_id,
						content = 'pellets',
						content_rowid = 'rowid'
					);`)
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "drifted-current.db")
			database := openTestDatabase(t, path)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			raw := openRawDatabase(t, path)
			test.mutate(t, raw)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			assertOpenFailureLeavesFileUnchanged(t, path, "schema_version_unsupported")
		})
	}
}

func TestEverySupportedProductionEndpointPassesSchemaPreflight(t *testing.T) {
	for version := 1; version <= LatestSchemaVersion; version++ {
		version := version
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), fmt.Sprintf("production-version-%d.db", version))
			database, err := openWithMigrations(context.Background(), path, migrations[:version])
			if err != nil {
				t.Fatalf("create production endpoint %d: %v", version, err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}

			database, err = Open(context.Background(), path)
			if err != nil {
				t.Fatalf("open production endpoint %d: %v", version, err)
			}
			defer database.Close()
			assertPragmaInt(t, database, "user_version", LatestSchemaVersion)
		})
	}
}

func TestOpeningLatestVersionPerformsNoPersistentWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "latest.db")
	db := openTestDatabase(t, path)
	identity := readApplicationMetadata(t, db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	observer := openRawDatabase(t, path)
	defer observer.Close()
	before := queryPragmaInt(t, observer, "data_version")

	db = openTestDatabase(t, path)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	after := queryPragmaInt(t, observer, "data_version")
	if after != before {
		t.Fatalf("PRAGMA data_version changed from %d to %d while opening the latest schema", before, after)
	}
	assertPragmaInt(t, observer, "user_version", LatestSchemaVersion)
	assertApplicationMetadataEqual(t, readApplicationMetadata(t, observer), identity)
}

func TestMigrationSequenceValidation(t *testing.T) {
	valid := []migration{{version: 1}, {version: 2}}
	if latest, err := validateMigrations(valid); err != nil || latest != 2 {
		t.Fatalf("validateMigrations(valid) = (%d, %v), want (2, nil)", latest, err)
	}

	for _, test := range []struct {
		name     string
		sequence []migration
	}{
		{name: "empty"},
		{name: "does not begin at one", sequence: []migration{{version: 2}}},
		{name: "duplicate", sequence: []migration{{version: 1}, {version: 1}}},
		{name: "gap", sequence: []migration{{version: 1}, {version: 3}}},
		{name: "out of order", sequence: []migration{{version: 1}, {version: 3}, {version: 2}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateMigrations(test.sequence); err == nil {
				t.Fatalf("validateMigrations(%v) unexpectedly succeeded", test.sequence)
			}
		})
	}
}

func TestEveryProductionMigrationFinishesWithForeignKeyAndFTSVerification(t *testing.T) {
	for version := 1; version <= len(migrations); version++ {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), fmt.Sprintf("version-%d.db", version))
			db, err := openWithMigrations(context.Background(), path, migrations[:version])
			if err != nil {
				t.Fatalf("apply production migrations through version %d: %v", version, err)
			}
			defer db.Close()

			assertPragmaInt(t, db, "user_version", version)
			assertQueryInt(t, db, "SELECT COUNT(*) FROM pragma_foreign_key_check", 0)
			assertExternalContentFTSIntegrity(t, db, "pellets_fts", true)
			assertExternalContentFTSIntegrity(t, db, "memories_fts", true)
		})
	}
}

func TestMigrationFTSVerificationFailureRollsBackSchemaAndVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fts-verification-rollback.db")
	sequence := []migration{{
		version: 1,
		name:    "unindexed-external-content",
		sql: `
			CREATE TABLE docs (id INTEGER PRIMARY KEY, text TEXT NOT NULL) STRICT;
			CREATE VIRTUAL TABLE docs_fts USING fts5(text, content = 'docs', content_rowid = 'id');
			INSERT INTO docs VALUES (1, 'missing from the derived index');`,
		ftsIndexes: []string{"docs_fts"},
	}}

	db, err := openWithMigrations(context.Background(), path, sequence)
	if db != nil {
		db.Close()
		t.Fatal("migration with a drifted FTS index unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_corrupt")
	assertMigrationRolledBack(t, path, "docs", 0)
}

func TestRunnerAppliesTwoStepTestSequenceExactlyOnce(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "two-step.db")
	sequence := twoStepTestMigrations()
	db, err := openWithMigrations(context.Background(), path, sequence)
	if err != nil {
		t.Fatalf("apply two-step test sequence: %v", err)
	}
	assertPragmaInt(t, db, "user_version", 2)
	assertQueryInt(t, db, "SELECT COUNT(*) FROM migration_probe", 2)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Both migrations contain non-idempotent DDL, so a stale version re-read
	// would fail here instead of silently applying either migration twice.
	db, err = openWithMigrations(context.Background(), path, sequence)
	if err != nil {
		t.Fatalf("reopen two-step test database: %v", err)
	}
	defer db.Close()
	assertPragmaInt(t, db, "user_version", 2)
	assertQueryInt(t, db, "SELECT COUNT(*) FROM migration_probe", 2)
}

func TestReleasedVersionOneFixtureInstallsOnlyMissingIdentityMetadata(t *testing.T) {
	tests := []struct {
		name             string
		preexistingKey   string
		preexistingValue string
	}{
		{name: "all known keys missing"},
		{name: "product is preserved", preexistingKey: "product", preexistingValue: "fixture-product"},
		{
			name:             "database ID is preserved",
			preexistingKey:   "database_id",
			preexistingValue: "11111111-2222-4333-8444-555555555555",
		},
		{name: "creation time is preserved", preexistingKey: "created_at_julian", preexistingValue: "2451544.5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "released-v1.db")
			copyDatabaseFixture(t, path, "testdata/released-v1.db")

			raw := openRawDatabase(t, path)
			assertPragmaInt(t, raw, "user_version", 1)
			if test.preexistingKey != "" {
				if _, err := raw.Exec(
					"INSERT INTO application_metadata(key, value) VALUES (?, ?)",
					test.preexistingKey,
					test.preexistingValue,
				); err != nil {
					t.Fatal(err)
				}
			}
			assertQueryInt(t, raw, `
				SELECT COUNT(*)
				FROM application_metadata
				WHERE key = 'fixture' AND value = 'released-v1'`, 1)
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}

			started := time.Now()
			db := openTestDatabase(t, path)
			finished := time.Now()
			defer db.Close()
			assertPragmaInt(t, db, "user_version", LatestSchemaVersion)

			metadata := readApplicationMetadata(t, db)
			if len(metadata) != 4 {
				t.Fatalf("upgraded metadata = %v, want three known keys and the fixture key", metadata)
			}
			if metadata["fixture"] != "released-v1" {
				t.Fatalf("fixture metadata = %q, want %q", metadata["fixture"], "released-v1")
			}
			for _, key := range []string{"database_id", "created_at_julian", "product"} {
				if _, exists := metadata[key]; !exists {
					t.Fatalf("upgraded metadata is missing %q: %v", key, metadata)
				}
			}
			if test.preexistingKey != "" && metadata[test.preexistingKey] != test.preexistingValue {
				t.Fatalf(
					"pre-existing %s = %q after upgrade, want %q",
					test.preexistingKey,
					metadata[test.preexistingKey],
					test.preexistingValue,
				)
			}

			if test.preexistingKey != "database_id" {
				assertRFC4122DatabaseID(t, metadata["database_id"])
			}
			if test.preexistingKey != "created_at_julian" {
				assertCapturedJulianText(t, db, metadata["created_at_julian"], started, finished)
			}
			wantProduct := "pellets"
			if test.preexistingKey == "product" {
				wantProduct = test.preexistingValue
			}
			if metadata["product"] != wantProduct {
				t.Fatalf("product = %q, want %q", metadata["product"], wantProduct)
			}
		})
	}
}

func TestDatabaseIdentityMigrationRollsBackMetadataAndVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "identity-rollback.db")
	copyDatabaseFixture(t, path, "testdata/released-v1.db")
	sequence := append([]migration(nil), migrations...)
	sequence[1].assert = func(context.Context, *sql.Conn) error {
		return errors.New("injected identity assertion failure")
	}

	db, err := openWithMigrations(context.Background(), path, sequence)
	if db != nil {
		db.Close()
		t.Fatal("identity migration with a failing assertion unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")

	raw := openRawDatabase(t, path)
	defer raw.Close()
	assertPragmaInt(t, raw, "user_version", 1)
	metadata := readApplicationMetadata(t, raw)
	if len(metadata) != 1 || metadata["fixture"] != "released-v1" {
		t.Fatalf("metadata after rolled-back identity migration = %v", metadata)
	}
}

func TestMigrationAssertionFailureRollsBackSchemaAndVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "assertion-failure.db")
	sequence := []migration{{
		version: 1,
		name:    "assertion-failure",
		sql:     "CREATE TABLE assertion_probe (value INTEGER) STRICT;",
		assert: func(context.Context, *sql.Conn) error {
			return errors.New("injected assertion failure")
		},
	}}

	db, err := openWithMigrations(context.Background(), path, sequence)
	if db != nil {
		db.Close()
		t.Fatal("migration with a failing assertion unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")
	assertMigrationRolledBack(t, path, "assertion_probe", 0)
}

func TestUserVersionFailureRollsBackSchemaAndVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "user-version-failure.db")
	sequence := []migration{{
		version: 1,
		name:    "user-version-failure",
		sql: `
			CREATE TABLE user_version_probe (value INTEGER) STRICT;
			PRAGMA query_only = ON;`,
	}}

	db, err := openWithMigrations(context.Background(), path, sequence)
	if db != nil {
		db.Close()
		t.Fatal("migration with a failing user_version write unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")
	assertMigrationRolledBack(t, path, "user_version_probe", 0)
}

func TestForeignKeyCheckFailureRollsBackSchemaAndVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "foreign-key-check.db")
	raw := openRawDatabase(t, path)
	mustExec(t, raw, `
		PRAGMA foreign_keys = OFF;
		CREATE TABLE parent (id INTEGER PRIMARY KEY) STRICT;
		CREATE TABLE child (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL REFERENCES parent(id)
		) STRICT;
		INSERT INTO child VALUES (1, 999);
		PRAGMA user_version = 1;`)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	sequence := []migration{
		{version: 1, name: "existing-fixture"},
		{
			version: 2,
			name:    "foreign-key-check",
			sql:     "CREATE TABLE foreign_key_probe (value INTEGER) STRICT;",
		},
	}
	db, err := openWithMigrations(context.Background(), path, sequence)
	if db != nil {
		db.Close()
		t.Fatal("migration with a foreign-key violation unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")
	assertMigrationRolledBack(t, path, "foreign_key_probe", 1)

	raw = openRawDatabase(t, path)
	defer raw.Close()
	assertQueryInt(t, raw, "SELECT COUNT(*) FROM pragma_foreign_key_check", 1)
}

func TestTwoProcessProductionMigrationRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.db")
	raw := openRawDatabase(t, path)
	var journalMode string
	if err := raw.QueryRow("PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	lockedPath := path + ".locked"
	releasePath := path + ".release"
	readyPath := path + ".second-ready"

	started := time.Now()
	first, firstOutput := migrationRaceProcess(path, lockedPath, releasePath, "")
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if first.ProcessState == nil {
			_ = first.Process.Kill()
		}
	})
	waitForFile(t, lockedPath)

	second, secondOutput := migrationRaceProcess(path, "", "", readyPath)
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if second.ProcessState == nil {
			_ = second.Process.Kill()
		}
	})
	waitForFile(t, readyPath)

	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Wait(); err != nil {
		t.Fatalf("first migration process: %v\n%s", err, firstOutput.String())
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second migration process: %v\n%s", err, secondOutput.String())
	}
	finished := time.Now()

	raw = openRawDatabase(t, path)
	defer raw.Close()
	assertPragmaInt(t, raw, "user_version", LatestSchemaVersion)
	assertFreshIdentityMetadata(t, raw, started, finished)
}

func TestMigrationBusyTimeoutIsBoundedTypedAndWriteFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration-busy.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:2])
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	locker, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = locker.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	started := time.Now()
	opened, openErr := Open(context.Background(), path)
	elapsed := time.Since(started)
	if opened != nil {
		opened.Close()
		t.Fatal("migration unexpectedly acquired a held writer lock")
	}
	assertDomainErrorCode(t, openErr, "database_busy")
	public := domain.PublicError(openErr)
	if public.Kind != domain.Conflict || !reflect.DeepEqual(public.Details, map[string]any{"operation": "migrate database"}) {
		t.Fatalf("migration busy error = %#v", public)
	}
	if elapsed < 4*time.Second || elapsed > 10*time.Second {
		t.Fatalf("migration busy wait elapsed %s, want bounded wait near configured five seconds", elapsed)
	}

	if _, err := locker.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	locked = false
	if err := locker.Close(); err != nil {
		t.Fatal(err)
	}
	assertPragmaInt(t, database, "user_version", 2)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name = 'root_path'`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM sqlite_schema WHERE name = 'project_workspaces'`, 0)
}

func TestMigrationRaceProcess(t *testing.T) {
	if os.Getenv("PELLETS_MIGRATION_RACE_PROCESS") != "1" {
		return
	}

	path := os.Getenv("PELLETS_MIGRATION_RACE_DATABASE")
	lockedPath := os.Getenv("PELLETS_MIGRATION_RACE_LOCKED")
	releasePath := os.Getenv("PELLETS_MIGRATION_RACE_RELEASE")
	readyPath := os.Getenv("PELLETS_MIGRATION_RACE_READY")
	sequence := migrationRaceTestMigrations(lockedPath, releasePath)
	hooks := migrationHooks{}
	if readyPath != "" {
		hooks.beforeLock = func(observedVersion int) error {
			if observedVersion != 0 {
				return fmt.Errorf("observed user_version %d before lock, want 0", observedVersion)
			}
			return os.WriteFile(readyPath, []byte("ready"), 0o600)
		}
	}

	db, err := openWithMigrationHooks(context.Background(), path, sequence, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationIsAtomic(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "atomic.db")
	sequence := []migration{{
		version: 1,
		name:    "sql-failure",
		sql: `
			CREATE TABLE sql_failure_probe (value INTEGER) STRICT;
			CREATE TABLE sql_failure_probe (value INTEGER) STRICT;`,
	}}
	db, err := openWithMigrations(context.Background(), path, sequence)
	if db != nil {
		db.Close()
		t.Fatal("migration with a mid-SQL failure unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")
	assertMigrationRolledBack(t, path, "sql_failure_probe", 0)
}

func twoStepTestMigrations() []migration {
	return []migration{
		{
			version: 1,
			name:    "test-initial",
			sql: `
				CREATE TABLE migration_probe (
					id INTEGER PRIMARY KEY,
					note TEXT NOT NULL
				) STRICT;
				INSERT INTO migration_probe VALUES (1, 'one');`,
			assert: func(ctx context.Context, conn *sql.Conn) error {
				if err := assertConnectionPragma(ctx, conn, "user_version", 0); err != nil {
					return err
				}
				return assertConnectionCount(ctx, conn, "SELECT COUNT(*) FROM migration_probe", 1)
			},
		},
		{
			version: 2,
			name:    "test-second",
			sql: `
				ALTER TABLE migration_probe
				ADD COLUMN applied_by INTEGER NOT NULL DEFAULT 2;
				INSERT INTO migration_probe(id, note) VALUES (2, 'two');`,
			assert: func(ctx context.Context, conn *sql.Conn) error {
				if err := assertConnectionPragma(ctx, conn, "user_version", 1); err != nil {
					return err
				}
				return assertConnectionCount(ctx, conn, `
					SELECT COUNT(*)
					FROM migration_probe
					WHERE applied_by = 2`, 2)
			},
		},
	}
}

func migrationRaceTestMigrations(lockedPath, releasePath string) []migration {
	sequence := append([]migration(nil), migrations...)
	initialAssertion := sequence[0].assert
	sequence[0].assert = func(ctx context.Context, conn *sql.Conn) error {
		if initialAssertion != nil {
			if err := initialAssertion(ctx, conn); err != nil {
				return err
			}
		}
		if err := assertConnectionPragma(ctx, conn, "user_version", 0); err != nil {
			return err
		}
		if lockedPath == "" {
			return nil
		}
		if err := os.WriteFile(lockedPath, []byte("locked"), 0o600); err != nil {
			return err
		}
		return waitForPath(ctx, releasePath, 10*time.Second)
	}
	return sequence
}

func migrationRaceProcess(path, lockedPath, releasePath, readyPath string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationRaceProcess$")
	cmd.Env = append(os.Environ(),
		"PELLETS_MIGRATION_RACE_PROCESS=1",
		"PELLETS_MIGRATION_RACE_DATABASE="+path,
		"PELLETS_MIGRATION_RACE_LOCKED="+lockedPath,
		"PELLETS_MIGRATION_RACE_RELEASE="+releasePath,
		"PELLETS_MIGRATION_RACE_READY="+readyPath,
	)
	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd, output
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	if err := waitForPath(context.Background(), path, 10*time.Second); err != nil {
		t.Fatal(err)
	}
}

func waitForPath(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %q", path)
		case <-ticker.C:
		}
	}
}

func assertFreshIdentityMetadata(t *testing.T, db *sql.DB, started, finished time.Time) map[string]string {
	t.Helper()
	metadata := readApplicationMetadata(t, db)
	if len(metadata) != 3 {
		t.Fatalf("fresh application metadata = %v, want exactly three identity keys", metadata)
	}
	for _, key := range []string{"database_id", "created_at_julian", "product"} {
		if _, exists := metadata[key]; !exists {
			t.Fatalf("fresh application metadata is missing %q: %v", key, metadata)
		}
	}
	if metadata["product"] != "pellets" {
		t.Fatalf("product = %q, want %q", metadata["product"], "pellets")
	}
	assertRFC4122DatabaseID(t, metadata["database_id"])
	assertCapturedJulianText(t, db, metadata["created_at_julian"], started, finished)
	return metadata
}

func readApplicationMetadata(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query("SELECT key, value FROM application_metadata ORDER BY key")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	metadata := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		metadata[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return metadata
}

func assertApplicationMetadataEqual(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("application metadata = %v, want unchanged %v", got, want)
	}
	for key, wantValue := range want {
		if gotValue, exists := got[key]; !exists || gotValue != wantValue {
			t.Fatalf("application metadata = %v, want unchanged %v", got, want)
		}
	}
}

func assertRFC4122DatabaseID(t *testing.T, value string) {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("database_id %q is not a UUID: %v", value, err)
	}
	if parsed.String() != value {
		t.Fatalf("database_id %q is not canonical RFC 4122 text", value)
	}
	if parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("database_id %q has variant %v, want RFC 4122", value, parsed.Variant())
	}
	if parsed.Version() != 4 {
		t.Fatalf("database_id %q has version %d, want randomly generated version 4", value, parsed.Version())
	}
}

func assertCapturedJulianText(t *testing.T, db *sql.DB, value string, started, finished time.Time) {
	t.Helper()
	created, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("created_at_julian %q is not numeric text: %v", value, err)
	}
	var valueType string
	if err := db.QueryRow(`
		SELECT typeof(value)
		FROM application_metadata
		WHERE key = 'created_at_julian'`).Scan(&valueType); err != nil {
		t.Fatal(err)
	}
	if valueType != "text" {
		t.Fatalf("created_at_julian SQLite type = %q, want text", valueType)
	}
	var parsedBySQLite float64
	if err := db.QueryRow("SELECT julianday(?)", value).Scan(&parsedBySQLite); err != nil {
		t.Fatalf("SQLite could not parse created_at_julian %q: %v", value, err)
	}
	if difference := math.Abs(parsedBySQLite - created); difference > float64(time.Millisecond)/float64(24*time.Hour) {
		t.Fatalf("SQLite parsed created_at_julian %q as %.15f, want %.15f", value, parsedBySQLite, created)
	}
	lower := julianDay(started.Add(-time.Second))
	upper := julianDay(finished.Add(time.Second))
	if created < lower || created > upper {
		t.Fatalf(
			"created_at_julian %.15f is outside initialization interval [%.15f, %.15f]",
			created,
			lower,
			upper,
		)
	}
}

func julianDay(value time.Time) float64 {
	return float64(value.UnixNano())/float64(24*time.Hour) + 2440587.5
}

func copyDatabaseFixture(t *testing.T, path, fixturePath string) {
	t.Helper()
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertMigrationRolledBack(t *testing.T, path, objectName string, wantVersion int) {
	t.Helper()
	db := openRawDatabase(t, path)
	defer db.Close()
	assertPragmaInt(t, db, "user_version", wantVersion)
	assertObjectAbsent(t, db, objectName)
}

func assertConnectionPragma(ctx context.Context, conn *sql.Conn, pragma string, want int) error {
	var got int
	if err := conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("PRAGMA %s = %d, want %d", pragma, got, want)
	}
	return nil
}

func assertConnectionCount(ctx context.Context, conn *sql.Conn, query string, want int) error {
	var got int
	if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("query count = %d, want %d", got, want)
	}
	return nil
}

func openTestDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	return db
}

func openRawDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func assertPragmaInt(t *testing.T, db *sql.DB, pragma string, want int) {
	t.Helper()
	got := queryPragmaInt(t, db, pragma)
	if got != want {
		t.Fatalf("PRAGMA %s = %d, want %d", pragma, got, want)
	}
}

func queryPragmaInt(t *testing.T, db *sql.DB, pragma string) int {
	t.Helper()
	var got int
	if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func assertQueryInt(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query result = %d, want %d\n%s", got, want, query)
	}
}

func assertObjectAbsent(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	assertQueryInt(t, db, `
		SELECT COUNT(*)
		FROM sqlite_schema
		WHERE name = '`+name+`'`, 0)
}

func assertDomainErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var public *domain.Error
	if !errors.As(err, &public) || public.Code != want {
		t.Fatalf("error = %v, want domain error %q", err, want)
	}
}

func assertOpenFailureLeavesFileUnchanged(t *testing.T, path, wantCode string) {
	t.Helper()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	db, openErr := Open(context.Background(), path)
	if db != nil {
		db.Close()
		t.Fatalf("Open(%q) unexpectedly returned a database", path)
	}
	assertDomainErrorCode(t, openErr, wantCode)
	if domain.PublicError(openErr).Kind != domain.Storage {
		t.Fatalf("Open(%q) error kind = %d, want storage", path, domain.PublicError(openErr).Kind)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("Open(%q) mutated a rejected database", path)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open(%q) left unexpected sidecar %q: %v", path, path+suffix, err)
		}
	}
}

func previouslyAcceptedPartialSchema(version int) string {
	if version < 3 {
		metadata := ""
		if version == 2 {
			metadata = `
				INSERT INTO application_metadata(key, value) VALUES
					('database_id', 'not-a-production-id'),
					('created_at_julian', 'not-a-Julian-day'),
					('product', 'not-pellets');`
		}
		return `
			CREATE TABLE application_metadata (key TEXT, value TEXT);
			CREATE TABLE projects (root_path TEXT);
			CREATE TABLE pellets (status TEXT);
			CREATE TABLE memories (value TEXT);` + metadata
	}
	return `
		CREATE TABLE application_metadata (key TEXT, value TEXT);
		INSERT INTO application_metadata(key, value) VALUES
			('database_id', 'not-a-production-id'),
			('created_at_julian', 'not-a-Julian-day'),
			('product', 'not-pellets');
		CREATE TABLE projects (git_common_dir TEXT);
		CREATE TABLE project_workspaces (value TEXT);
		CREATE TABLE pellets (status TEXT, workspace_id INTEGER);
		CREATE TABLE memories (value TEXT);
		CREATE UNIQUE INDEX pellets_workspace_in_progress_idx
			ON pellets(workspace_id) WHERE status = 'in_progress';`
}

func rewriteSchemaObject(t *testing.T, database *sql.DB, name, old, replacement string) {
	t.Helper()
	mustExec(t, database, "PRAGMA writable_schema = ON")
	result, err := database.Exec(`
		UPDATE sqlite_schema
		SET sql = replace(sql, ?, ?)
		WHERE name = ? AND instr(sql, ?) > 0`, old, replacement, name, old)
	if err != nil {
		t.Fatalf("rewrite schema object %q: %v", name, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("read rewritten schema object count %q: %v", name, err)
	}
	if affected != 1 {
		t.Fatalf("rewritten schema object count %q = %d, want 1", name, affected)
	}
	mustExec(t, database, "PRAGMA writable_schema = OFF")
}

func recreateApplicationMetadata(t *testing.T, database *sql.DB, createSQL string) {
	t.Helper()
	mustExec(t, database, "ALTER TABLE application_metadata RENAME TO application_metadata_old")
	mustExec(t, database, createSQL)
	mustExec(t, database, `
		INSERT INTO application_metadata(key, value)
		SELECT key, value FROM application_metadata_old`)
	mustExec(t, database, "DROP TABLE application_metadata_old")
}

func createCorruptDatabaseFixture(t *testing.T, path string) {
	t.Helper()
	db := openTestDatabase(t, path)
	mustExec(t, db, `
		CREATE TABLE corruption_probe (
			id INTEGER PRIMARY KEY,
			payload BLOB NOT NULL
		) STRICT;
		INSERT INTO corruption_probe(payload) VALUES (randomblob(2048));`)
	var rootPage, pageSize int64
	if err := db.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name = 'corruption_probe'").Scan(&rootPage); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if rootPage <= 1 || pageSize <= 0 {
		db.Close()
		t.Fatalf("corruption fixture root page/page size = %d/%d", rootPage, pageSize)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{0xff}, (rootPage-1)*pageSize); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertExternalContentFTSIntegrity(t *testing.T, db *sql.DB, table string, wantOK bool) {
	t.Helper()
	connection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	err = verifyExternalContentFTSIndex(context.Background(), connection, table)
	if wantOK && err != nil {
		t.Fatalf("FTS integrity check for %s: %v", table, err)
	}
	if !wantOK && err == nil {
		t.Fatalf("FTS integrity check for %s unexpectedly passed", table)
	}
}

func assertSchemaObjects(t *testing.T, db *sql.DB) {
	t.Helper()
	wantTables := []string{
		"application_metadata",
		"memories",
		"memories_fts",
		"memories_fts_config",
		"memories_fts_data",
		"memories_fts_docsize",
		"memories_fts_idx",
		"pellets",
		"pellets_fts",
		"pellets_fts_config",
		"pellets_fts_data",
		"pellets_fts_docsize",
		"pellets_fts_idx",
		"project_workspaces",
		"projects",
	}
	wantIndexes := []string{
		"memories_project_approval_idx",
		"pellets_active_priority_idx",
		"pellets_closed_completed_idx",
		"pellets_workspace_in_progress_idx",
	}
	assertObjectNames(t, db, "table", wantTables)
	assertObjectNames(t, db, "index", wantIndexes)
	assertObjectNames(t, db, "trigger", nil)
}

func assertObjectNames(t *testing.T, db *sql.DB, objectType string, want []string) {
	t.Helper()
	rows, err := db.Query(`
		SELECT name
		FROM sqlite_schema
		WHERE type = ? AND name NOT LIKE 'sqlite_%'
		ORDER BY name`, objectType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s objects = %v, want %v", objectType, got, want)
	}
}

func assertStrictTables(t *testing.T, db *sql.DB) {
	t.Helper()
	want := []string{"application_metadata", "memories", "pellets", "project_workspaces", "projects"}
	rows, err := db.Query("PRAGMA table_list")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	strict := make(map[string]int)
	for rows.Next() {
		var schema, name, tableType string
		var columns, withoutRowID, isStrict int
		if err := rows.Scan(&schema, &name, &tableType, &columns, &withoutRowID, &isStrict); err != nil {
			t.Fatal(err)
		}
		if schema == "main" {
			strict[name] = isStrict
		}
	}
	for _, name := range want {
		if strict[name] != 1 {
			t.Fatalf("table %q strict flag = %d, want 1", name, strict[name])
		}
	}
}

func assertPartialIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	type properties struct {
		unique  int
		partial int
	}
	want := map[string]properties{
		"pellets_workspace_in_progress_idx": {unique: 1, partial: 1},
		"pellets_active_priority_idx":       {unique: 1, partial: 1},
		"pellets_closed_completed_idx":      {unique: 0, partial: 1},
	}
	rows, err := db.Query("PRAGMA index_list('pellets')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]properties)
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if _, expected := want[name]; expected {
			got[name] = properties{unique: unique, partial: partial}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("pellet partial indexes = %v, want %v", got, want)
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Fatalf("index %q properties = %+v, want %+v", name, got[name], expected)
		}
	}
}

func assertFTSIndexes(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `
		INSERT INTO projects(
			project_id, code, git_common_dir, git_common_dir_relative, created_at, updated_at
		) VALUES (10, 'fts', 'fts/.git', 1, 1, 1);
		INSERT INTO project_workspaces(
			workspace_id, project_id, root_path, root_path_relative,
			git_dir, git_dir_relative, created_at, updated_at
		) VALUES (1000, 10, 'fts', 1, 'fts/.git', 1, 1, 1);
		INSERT INTO pellets(rowid, project_id, number, title, description, external_id,
		                    status, priority, created_at, updated_at)
		VALUES (100, 10, 1, 'alpha-beta', 'searchable pellet', 'ext_id',
		        'open', 1024, 1, 1);
		INSERT INTO pellets_fts(rowid, title, description, external_id)
		VALUES (100, 'alpha-beta', 'searchable pellet', 'ext_id');
		INSERT INTO memories(memory_id, project_id, text, created_by, created_at, updated_at)
		VALUES (200, 10, 'remember alpha-beta', 'agent', 1, 1);
		INSERT INTO memories_fts(rowid, text)
		VALUES (200, 'remember alpha-beta');`)

	for _, test := range []struct {
		query string
		want  int
	}{
		{query: `SELECT rowid FROM pellets_fts WHERE pellets_fts MATCH '"alpha-beta"'`, want: 100},
		{query: `SELECT rowid FROM pellets_fts WHERE pellets_fts MATCH 'ext_id'`, want: 100},
		{query: `SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"alpha-beta"'`, want: 200},
	} {
		var got int
		if err := db.QueryRow(test.query).Scan(&got); err != nil {
			t.Fatalf("FTS query %q: %v", test.query, err)
		}
		if got != test.want {
			t.Fatalf("FTS query %q = %d, want %d", test.query, got, test.want)
		}
	}
}

func mustInsertProjectAndWorkspace(t *testing.T, db *sql.DB, projectID, workspaceID int, code, root string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO projects(
			project_id, code, git_common_dir, git_common_dir_relative, created_at, updated_at
		) VALUES (?, ?, ?, 1, 1, 1)`, projectID, code, root+"/.git"); err != nil {
		t.Fatalf("insert project %d: %v", projectID, err)
	}
	if _, err := db.Exec(`
		INSERT INTO project_workspaces(
			workspace_id, project_id, root_path, root_path_relative,
			git_dir, git_dir_relative, created_at, updated_at
		) VALUES (?, ?, ?, 1, ?, 1, 1, 1)`, workspaceID, projectID, root, root+"/.git"); err != nil {
		t.Fatalf("insert project/workspace %d/%d: %v", projectID, workspaceID, err)
	}
}

func mustInsertPellet(t *testing.T, db *sql.DB, projectID, number int, status string, workspaceID, priority *int, completedAt *float64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO pellets(project_id, workspace_id, number, title, status, priority, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?)`,
		projectID, workspaceID, number, "pellet", status, priority, completedAt,
	); err != nil {
		t.Fatalf("insert pellet project=%d number=%d status=%s: %v", projectID, number, status, err)
	}
}

func assertInsertPelletFails(t *testing.T, db *sql.DB, projectID, number int, status string, workspaceID, priority *int, completedAt *float64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO pellets(project_id, workspace_id, number, title, status, priority, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, 1, ?)`,
		projectID, workspaceID, number, "pellet", status, priority, completedAt,
	); err == nil {
		t.Fatalf("insert pellet project=%d number=%d status=%s unexpectedly succeeded", projectID, number, status)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("Exec failed: %v\n%s", err, query)
	}
}

func assertExecFails(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err == nil {
		t.Fatalf("Exec unexpectedly succeeded:\n%s", query)
	}
}

func intPtr(value int) *int {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}
