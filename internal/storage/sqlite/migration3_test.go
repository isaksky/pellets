package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDifferentWorkspacesCanConcurrentlyCommitInProgressPellets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace-concurrency.db")
	first := openTestDatabase(t, path)
	defer first.Close()
	second := openTestDatabase(t, path)
	defer second.Close()
	mustInsertProjectAndWorkspace(t, first, 1, 11, "shared", "main")
	mustExec(t, first, `
		INSERT INTO project_workspaces(
			workspace_id, project_id, root_path, root_path_relative,
			git_dir, git_dir_relative, created_at, updated_at
		) VALUES (12, 1, 'linked', 1, 'main/.git/worktrees/linked', 1, 1, 1)`)

	start := make(chan struct{})
	errors := make(chan error, 2)
	insert := func(database *sql.DB, rowID, number, workspaceID, priority int) {
		<-start
		_, err := database.Exec(`
			INSERT INTO pellets(
				rowid, project_id, workspace_id, number, title, status,
				priority, created_at, updated_at
			) VALUES (?, 1, ?, ?, 'active', 'in_progress', ?, 1, 1)`,
			rowID, workspaceID, number, priority)
		errors <- err
	}
	go insert(first, 101, 1, 11, 1024)
	go insert(second, 102, 2, 12, 2048)
	close(start)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent workspace insert: %v", err)
		}
	}
	assertQueryInt(t, first, `SELECT COUNT(*) FROM pellets WHERE status = 'in_progress'`, 2)
}

func TestProjectWorkspaceMigrationPreservesAuthoritativeAndFTSState(t *testing.T) {
	t.Parallel()

	fixturePath := "testdata/released-v1.db"
	frozenBefore, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "workspace-upgrade.db")
	copyDatabaseFixture(t, path, fixturePath)
	raw := openRawDatabase(t, path)
	seedVersionOneWorkspaceFixture(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	database := openTestDatabase(t, path)
	defer database.Close()
	assertPragmaInt(t, database, "user_version", LatestSchemaVersion)
	assertQueryInt(t, database, `
		SELECT COUNT(*) FROM projects
		WHERE project_id = 7 AND code = 'alpha' AND git_common_dir = 'repos/alpha/.git'
		  AND git_common_dir_relative = 1 AND next_pellet_number = 10
		  AND created_at = 100 AND updated_at = 110`, 1)
	assertQueryInt(t, database, `
		SELECT COUNT(*) FROM project_workspaces
		WHERE workspace_id = 7 AND project_id = 7 AND root_path = 'repos/alpha'
		  AND root_path_relative = 1 AND git_dir = 'repos/alpha/.git'
		  AND git_dir_relative = 1 AND created_at = 100 AND updated_at = 110`, 1)
	assertQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE rowid = 101 AND project_id = 7 AND number = 3 AND status = 'open'
		  AND priority = 1024 AND workspace_id IS NULL AND created_at = 101 AND updated_at = 102`, 1)
	assertQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE rowid = 102 AND project_id = 7 AND number = 4 AND status = 'in_progress'
		  AND priority = 2048 AND workspace_id = 7 AND created_at = 103 AND updated_at = 104`, 1)
	assertQueryInt(t, database, `
		SELECT COUNT(*) FROM memories
		WHERE memory_id = 42 AND project_id = 7 AND text = 'memory migration-anchor'
		  AND created_by = 'agent' AND approved_at IS NULL AND created_at = 105 AND updated_at = 105`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM application_metadata WHERE key = 'custom' AND value = 'preserved'`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM pellets_fts WHERE pellets_fts MATCH '"migration-anchor"'`, 2)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH '"migration-anchor"'`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM pragma_foreign_key_check`, 0)

	var nextMemoryID int
	if err := database.QueryRow(`
		INSERT INTO memories(project_id, text, created_by, created_at, updated_at)
		VALUES (7, 'after migration', 'agent', 120, 120)
		RETURNING memory_id`).Scan(&nextMemoryID); err != nil {
		t.Fatal(err)
	}
	if nextMemoryID != 100 {
		t.Fatalf("memory ID after preserved sequence = %d, want 100", nextMemoryID)
	}

	frozenAfter, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frozenAfter, frozenBefore) {
		t.Fatal("released-v1.db changed during forward-migration test")
	}
}

func TestProjectWorkspaceMigrationAssertionFailureRestoresVersionTwo(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workspace-rollback.db")
	copyDatabaseFixture(t, path, "testdata/released-v1.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:2])
	if err != nil {
		t.Fatal(err)
	}
	seedVersionOneWorkspaceFixture(t, database)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	sequence := append([]migration(nil), migrations...)
	sequence[2].assert = func(context.Context, *sql.Conn) error {
		return errors.New("injected workspace assertion failure")
	}
	database, err = openWithMigrations(context.Background(), path, sequence)
	if database != nil {
		database.Close()
		t.Fatal("migration with a failing workspace assertion unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")

	raw := openRawDatabase(t, path)
	defer raw.Close()
	assertPragmaInt(t, raw, "user_version", 2)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM pragma_table_info('projects') WHERE name = 'root_path'`, 1)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM sqlite_schema WHERE name = 'project_workspaces'`, 0)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM pellets WHERE rowid IN (101, 102)`, 2)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM pellets_fts WHERE pellets_fts MATCH '"migration-anchor"'`, 2)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH '"migration-anchor"'`, 1)
}

func seedVersionOneWorkspaceFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	mustExec(t, database, `
		INSERT OR IGNORE INTO application_metadata(key, value) VALUES ('custom', 'preserved');
		INSERT OR IGNORE INTO projects(
			project_id, code, root_path, next_pellet_number, created_at, updated_at
		) VALUES (7, 'alpha', 'repos/alpha', 10, 100, 110);
		INSERT OR IGNORE INTO pellets(
			rowid, project_id, number, title, description, external_id, group_id,
			status, priority, created_at, updated_at, completed_at
		) VALUES
			(101, 7, 3, 'open migration-anchor', 'open description', 'ext-open', 'group-a',
			 'open', 1024, 101, 102, NULL),
			(102, 7, 4, 'active pellet', 'active migration-anchor', 'ext-active', 'group-a',
			 'in_progress', 2048, 103, 104, NULL);
		INSERT OR IGNORE INTO pellets_fts(rowid, title, description, external_id) VALUES
			(101, 'open migration-anchor', 'open description', 'ext-open'),
			(102, 'active pellet', 'active migration-anchor', 'ext-active');
		INSERT OR IGNORE INTO memories(
			memory_id, project_id, text, created_by, approved_at, created_at, updated_at
		) VALUES (42, 7, 'memory migration-anchor', 'agent', NULL, 105, 105);
		INSERT OR IGNORE INTO memories_fts(rowid, text) VALUES (42, 'memory migration-anchor');
		UPDATE sqlite_sequence SET seq = 99 WHERE name = 'memories';`)
}
