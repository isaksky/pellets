package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestProjectCodeRedirectMigrationPreservesStableRowsAndAddsEmptyNamespace(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "redirect-upgrade.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:3])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, database, 7, 17, "legacy", "repo")
	mustInsertPellet(t, database, 7, 23, "open", nil, intPtr(1024), nil)
	mustExec(t, database, `
		INSERT INTO pellets_fts(rowid, title, description, external_id)
		SELECT rowid, title, description, external_id
		FROM pellets WHERE project_id = 7 AND number = 23`)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database = openTestDatabase(t, path)
	defer database.Close()
	assertPragmaInt(t, database, "user_version", LatestSchemaVersion)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM projects WHERE project_id = 7 AND code = 'legacy'`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM project_workspaces WHERE workspace_id = 17 AND project_id = 7`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM pellets WHERE project_id = 7 AND number = 23`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM project_code_redirects`, 0)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM pragma_foreign_key_check`, 0)
}

func TestProjectCodeRedirectMigrationAssertionFailureRestoresVersionThree(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "redirect-rollback.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:3])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, database, 1, 1, "legacy", "repo")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	sequence := append([]migration(nil), migrations...)
	sequence[3].assert = func(context.Context, *sql.Conn) error {
		return errors.New("injected redirect assertion failure")
	}
	database, err = openWithMigrations(context.Background(), path, sequence)
	if database != nil {
		database.Close()
		t.Fatal("migration with a failing redirect assertion unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")

	raw := openRawDatabase(t, path)
	defer raw.Close()
	assertPragmaInt(t, raw, "user_version", 3)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM sqlite_schema WHERE name = 'project_code_redirects'`, 0)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM projects WHERE project_id = 1 AND code = 'legacy'`, 1)
}

func TestProjectCodeRedirectForeignKeyCannotDangle(t *testing.T) {
	t.Parallel()
	database := openTestDatabase(t, filepath.Join(t.TempDir(), "redirect-cascade.db"))
	defer database.Close()
	mustInsertProjectAndWorkspace(t, database, 1, 1, "canonical", "repo")
	mustExec(t, database, `INSERT INTO project_code_redirects(code, project_id, created_at, updated_at) VALUES ('former', 1, 1, 1)`)
	mustExec(t, database, `DELETE FROM project_workspaces WHERE project_id = 1`)
	mustExec(t, database, `DELETE FROM projects WHERE project_id = 1`)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM project_code_redirects`, 0)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM pragma_foreign_key_check`, 0)
}
