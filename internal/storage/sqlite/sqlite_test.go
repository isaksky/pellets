package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	var version int
	var name, checksum string
	var appliedAt float64
	if err := db.QueryRow(`
		SELECT version, name, checksum, applied_at
		FROM schema_migrations`).Scan(&version, &name, &checksum, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if version != LatestSchemaVersion || name != "initial" {
		t.Fatalf("migration = (%d, %q), want (%d, %q)", version, name, LatestSchemaVersion, "initial")
	}
	const wantMigration1Checksum = "8e5ac8fff4071c360ccdb8410b0521acee7ead7c2b8df33b63bf3547172d3056"
	if got := migrationChecksum(migrations[0]); got != wantMigration1Checksum {
		t.Fatalf("embedded migration checksum = %q, want fixture %q", got, wantMigration1Checksum)
	}
	if checksum != wantMigration1Checksum {
		t.Fatalf("recorded migration checksum = %q, want %q", checksum, wantMigration1Checksum)
	}
	if appliedAt <= 0 {
		t.Fatalf("migration applied_at = %v, want positive Julian day", appliedAt)
	}

	assertSchemaObjects(t, db)
	assertStrictTables(t, db)
	assertPartialIndexes(t, db)
	assertFTSIndexes(t, db)

	// Reopening verifies the recorded checksum and leaves migration 1 singular.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDatabase(t, path)
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migration count after reopen = %d, want 1", count)
	}
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

func TestNewerSchemaIsRejectedWithoutWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "newer.db")
	raw := openRawDatabase(t, path)
	mustExec(t, raw, `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			checksum TEXT NOT NULL,
			applied_at REAL NOT NULL
		) STRICT;
		CREATE TABLE marker (value TEXT NOT NULL) STRICT;
		INSERT INTO schema_migrations VALUES (2, 'future', 'future-checksum', 1);
		INSERT INTO marker VALUES ('unchanged');`)
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
		t.Fatal("Open returned a database for a newer schema")
	}
	var public *domain.Error
	if !errors.As(err, &public) || public.Code != "schema_too_new" {
		t.Fatalf("Open error = %v, want schema_too_new", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("newer database file changed while being rejected")
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, statErr := os.Stat(path + suffix); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unexpected sidecar %q after rejection: %v", path+suffix, statErr)
		}
	}
}

func TestMigrationIsAtomic(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "atomic.db")
	raw := openRawDatabase(t, path)
	mustExec(t, raw, "CREATE TABLE projects (sentinel TEXT) STRICT")
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(context.Background(), path)
	if db != nil {
		db.Close()
		t.Fatal("Open succeeded despite a conflicting pre-migration table")
	}
	var public *domain.Error
	if !errors.As(err, &public) || public.Code != "database_migration_failed" {
		t.Fatalf("Open error = %v, want database_migration_failed", err)
	}

	raw = openRawDatabase(t, path)
	defer raw.Close()
	for _, name := range []string{"application_metadata", "schema_migrations", "pellets", "memories", "pellets_fts", "memories_fts"} {
		var count int
		if err := raw.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("partially migrated object %q survived rollback", name)
		}
	}
	var sentinelColumns int
	if err := raw.QueryRow("SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name = 'sentinel'").Scan(&sentinelColumns); err != nil {
		t.Fatal(err)
	}
	if sentinelColumns != 1 {
		t.Fatal("preexisting projects table did not survive migration rollback")
	}
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
	var got int
	if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %d, want %d", pragma, got, want)
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
		"schema_migrations",
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
	want := []string{"application_metadata", "memories", "pellets", "projects", "schema_migrations"}
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
