package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pellets/internal/domain"
)

func TestOpenMigratesRealFileAndConfiguresRuntime(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "pellets.db")
	db := openTestDatabase(t, path)
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

	// Reopening at the latest version applies no migration.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDatabase(t, path)
	defer db.Close()
	assertPragmaInt(t, db, "user_version", LatestSchemaVersion)
}

func TestMemoryIDsAreNeverReusedAfterRemoval(t *testing.T) {
	t.Parallel()

	db := openTestDatabase(t, filepath.Join(t.TempDir(), "memory-ids.db"))
	defer db.Close()

	mustExec(t, db, `
		INSERT INTO projects(project_id, code, root_path, created_at, updated_at)
		VALUES (1, 'memory', 'memory', 1, 1)`)

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

	mustExec(t, db, `
		INSERT INTO projects(project_id, code, root_path, created_at, updated_at)
		VALUES (1, 'one', 'one', 1, 1), (2, 'two', 'two', 1, 1)`)
	mustInsertPellet(t, db, 1, 1, "open", intPtr(1024), nil)

	// Active priority and in-progress uniqueness are project-scoped partial
	// indexes, so duplicates fail within a project but work across projects.
	assertInsertPelletFails(t, db, 1, 2, "open", intPtr(1024), nil)
	mustInsertPellet(t, db, 1, 3, "in_progress", intPtr(2048), nil)
	assertInsertPelletFails(t, db, 1, 4, "in_progress", intPtr(3072), nil)
	mustInsertPellet(t, db, 2, 1, "open", intPtr(1024), nil)
	mustInsertPellet(t, db, 2, 2, "in_progress", intPtr(2048), nil)

	assertInsertPelletFails(t, db, 1, 5, "closed", intPtr(4096), floatPtr(2))
	assertInsertPelletFails(t, db, 1, 6, "maybe_later", intPtr(4096), nil)
	assertInsertPelletFails(t, db, 1, 7, "open", nil, nil)
	assertInsertPelletFails(t, db, 1, 8, "closed", nil, nil)
	mustInsertPellet(t, db, 1, 9, "closed", nil, floatPtr(2))
	mustInsertPellet(t, db, 1, 10, "maybe_later", nil, nil)

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

func TestOpeningLatestVersionPerformsNoPersistentWrite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "latest.db")
	db := openTestDatabase(t, path)
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

func TestReleasedVersionOneFixtureUpgradesWithInjectedMigration(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "released-v1.db")
	copyDatabaseFixture(t, path, "testdata/released-v1.db")

	raw := openRawDatabase(t, path)
	assertPragmaInt(t, raw, "user_version", 1)
	assertQueryInt(t, raw, `
		SELECT COUNT(*)
		FROM application_metadata
		WHERE key = 'fixture' AND value = 'released-v1'`, 1)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	sequence := append([]migration(nil), migrations...)
	sequence = append(sequence, migration{
		version: 2,
		name:    "fixture-upgrade-probe",
		sql: `
			CREATE TABLE fixture_upgrade_probe (
				value TEXT PRIMARY KEY
			) STRICT;
			INSERT INTO fixture_upgrade_probe VALUES ('applied-once');`,
		assert: func(ctx context.Context, conn *sql.Conn) error {
			if err := assertConnectionPragma(ctx, conn, "user_version", 1); err != nil {
				return err
			}
			return assertConnectionCount(ctx, conn, "SELECT COUNT(*) FROM fixture_upgrade_probe", 1)
		},
	})

	db, err := openWithMigrations(context.Background(), path, sequence)
	if err != nil {
		t.Fatalf("upgrade released v1 fixture: %v", err)
	}
	defer db.Close()
	assertPragmaInt(t, db, "user_version", 2)
	assertQueryInt(t, db, "SELECT COUNT(*) FROM fixture_upgrade_probe", 1)
	assertQueryInt(t, db, `
		SELECT COUNT(*)
		FROM application_metadata
		WHERE key = 'fixture' AND value = 'released-v1'`, 1)
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

func TestTwoProcessMigrationRace(t *testing.T) {
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

	raw = openRawDatabase(t, path)
	defer raw.Close()
	assertPragmaInt(t, raw, "user_version", 2)
	assertQueryInt(t, raw, "SELECT COUNT(*) FROM migration_race_probe", 2)
	var applied string
	if err := raw.QueryRow(`
		SELECT group_concat(version, ',')
		FROM (SELECT version FROM migration_race_probe ORDER BY version)`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != "1,2" {
		t.Fatalf("applied race migrations = %q, want %q", applied, "1,2")
	}
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
	return []migration{
		{
			version: 1,
			name:    "race-initial",
			sql: `
				CREATE TABLE migration_race_probe (
					version INTEGER PRIMARY KEY
				) STRICT;
				INSERT INTO migration_race_probe VALUES (1);`,
			assert: func(ctx context.Context, conn *sql.Conn) error {
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
			},
		},
		{
			version: 2,
			name:    "race-second",
			sql:     "INSERT INTO migration_race_probe VALUES (2);",
			assert: func(ctx context.Context, conn *sql.Conn) error {
				return assertConnectionPragma(ctx, conn, "user_version", 1)
			},
		},
	}
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
		"projects",
	}
	wantIndexes := []string{
		"memories_project_approval_idx",
		"pellets_active_priority_idx",
		"pellets_closed_completed_idx",
		"pellets_one_in_progress_idx",
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
	want := []string{"application_metadata", "memories", "pellets", "projects"}
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
		"pellets_one_in_progress_idx":  {unique: 1, partial: 1},
		"pellets_active_priority_idx":  {unique: 1, partial: 1},
		"pellets_closed_completed_idx": {unique: 0, partial: 1},
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
		INSERT INTO projects(project_id, code, root_path, created_at, updated_at)
		VALUES (10, 'fts', 'fts', 1, 1);
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

func mustInsertPellet(t *testing.T, db *sql.DB, projectID, number int, status string, priority *int, completedAt *float64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO pellets(project_id, number, title, status, priority, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, 1, 1, ?)`,
		projectID, number, "pellet", status, priority, completedAt,
	); err != nil {
		t.Fatalf("insert pellet project=%d number=%d status=%s: %v", projectID, number, status, err)
	}
}

func assertInsertPelletFails(t *testing.T, db *sql.DB, projectID, number int, status string, priority *int, completedAt *float64) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO pellets(project_id, number, title, status, priority, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, 1, 1, ?)`,
		projectID, number, "pellet", status, priority, completedAt,
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
