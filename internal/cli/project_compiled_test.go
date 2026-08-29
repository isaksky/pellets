package cli

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
	storageSQLite "pellets/internal/storage/sqlite"
)

func TestCompiledCLIUsageValidationDoesNotDependOnDatabase(t *testing.T) {
	executable := buildTestExecutable(t)

	withoutDatabase := filepath.Join(t.TempDir(), "without database")
	withDatabase := filepath.Join(t.TempDir(), "with database")
	for _, directory := range []string{withoutDatabase, withDatabase} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if stdout, stderr, exit := runCompiledCLI(t, executable, withDatabase, "init-db"); exit != 0 || stdout != exactInitDBSuccess(discovery.DatabasePath(withDatabase)) || stderr != "" {
		t.Fatalf("compiled fixture init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	tests := []struct {
		name string
		args []string
		code string
	}{
		{name: "forbidden project override", args: []string{"--project", "foo", "project", "list"}, code: "project_not_allowed"},
		{name: "conflicting project selection", args: []string{"--project", "foo", "project", "show", "bar"}, code: "conflicting_project_selection"},
		{name: "malformed global project", args: []string{"--project", "bad_code", "project", "show"}, code: "invalid_project_code"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			withoutStdout, withoutStderr, withoutExit := runCompiledCLI(t, executable, withoutDatabase, test.args...)
			withStdout, withStderr, withExit := runCompiledCLI(t, executable, withDatabase, test.args...)
			if withoutExit != 2 || withoutStdout != "" {
				t.Fatalf("without database = exit %d stdout %q stderr %q", withoutExit, withoutStdout, withoutStderr)
			}
			if withExit != withoutExit || withStdout != withoutStdout || withStderr != withoutStderr {
				t.Fatalf(
					"result depends on database presence:\nwithout = exit %d stdout %q stderr %q\nwith = exit %d stdout %q stderr %q",
					withoutExit,
					withoutStdout,
					withoutStderr,
					withExit,
					withStdout,
					withStderr,
				)
			}
			assertCompactErrorCode(t, withoutStderr, test.code)
		})
	}

	stdout, stderr, exit := runCompiledCLI(t, executable, withoutDatabase, "--project", "foo", "project", "show")
	if exit != 3 || stdout != "" {
		t.Fatalf("valid project show = exit %d stdout %q stderr %q, want discovery failure", exit, stdout, stderr)
	}
	assertCompactErrorCode(t, stderr, "database_not_found")
}

func TestCompiledCLIRejectsPartialCurrentSchemaWithoutWrites(t *testing.T) {
	executable := buildTestExecutable(t)
	root := t.TempDir()
	databasePath := discovery.DatabasePath(root)
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(fmt.Sprintf(`
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
			ON pellets(workspace_id) WHERE status = 'in_progress';
		PRAGMA user_version = %d;`, storageSQLite.LatestSchemaVersion)); err != nil {
		database.Close()
		t.Fatalf("create partial current schema: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runCompiledCLI(t, executable, root, "project", "list")
	if exit != 5 || stdout != "" {
		t.Fatalf("project list = exit %d stdout %q stderr %q, want storage failure", exit, stdout, stderr)
	}
	assertCompactErrorCode(t, stderr, "schema_version_unsupported")
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("compiled CLI mutated the rejected database")
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(databasePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("compiled CLI left unexpected sidecar %q: %v", databasePath+suffix, err)
		}
	}
}

func TestCompiledCLIRejectsUnexpectedPersistentTriggerWithoutWrites(t *testing.T) {
	executable := buildTestExecutable(t)
	root := t.TempDir()
	databasePath := discovery.DatabasePath(root)
	if stdout, stderr, exit := runCompiledCLI(t, executable, root, "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("compiled init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER unexpected_persistent_trigger
		AFTER INSERT ON memories
		BEGIN
			DELETE FROM memories WHERE memory_id = NEW.memory_id;
		END`); err != nil {
		database.Close()
		t.Fatalf("create unexpected persistent trigger: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exit := runCompiledCLI(t, executable, root, "project", "list")
	if exit != 5 || stdout != "" {
		t.Fatalf("project list = exit %d stdout %q stderr %q, want storage failure", exit, stdout, stderr)
	}
	assertCompactErrorCode(t, stderr, "schema_version_unsupported")
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("compiled CLI mutated the trigger-bearing database")
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(databasePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("compiled CLI left unexpected sidecar %q: %v", databasePath+suffix, err)
		}
	}

	database, err = sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var triggerCount int
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'trigger' AND name = 'unexpected_persistent_trigger'`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 1 {
		t.Fatalf("unexpected persistent trigger count = %d, want 1", triggerCount)
	}
}
