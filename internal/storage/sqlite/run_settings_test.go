package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestWorkspaceRunSettingsMigrationPreservesWorkspacesAndAddsNoCredentialFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings-upgrade.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:5])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, database, 7, 17, "stable", "repo")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = openTestDatabase(t, path)
	defer database.Close()
	assertPragmaInt(t, database, "user_version", LatestSchemaVersion)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM project_workspaces WHERE workspace_id = 17 AND project_id = 7`, 1)
	assertQueryInt(t, database, `SELECT COUNT(*) FROM workspace_run_settings`, 0)
	rows, err := database.Query(`SELECT name FROM pragma_table_info('workspace_run_settings') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	want := []string{"workspace_id", "codex_executable", "model", "reasoning_effort", "max_message_bytes", "event_buffer", "max_pending", "stderr_bytes", "revision", "created_at", "updated_at"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("settings columns = %v, want %v", names, want)
	}
}

func TestWorkspaceRunSettingsMigrationFailureRollsBackVersionFive(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings-rollback.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:5])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, database, 1, 2, "stable", "repo")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	sequence := append([]migration(nil), migrations...)
	sequence[5].assert = func(context.Context, *sql.Conn) error { return errors.New("injected settings assertion failure") }
	database, err = openWithMigrations(context.Background(), path, sequence)
	if database != nil {
		database.Close()
		t.Fatal("migration with a failing settings assertion unexpectedly succeeded")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")
	assertMigrationRolledBack(t, path, "workspace_run_settings", 5)
}

func TestWorkspaceRunSettingsRoundTripConflictAndRenameStableIdentity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.db")
	raw := openTestDatabase(t, path)
	mustInsertProjectAndWorkspace(t, raw, 7, 17, "before", "repo")
	mustInsertProjectAndWorkspace(t, raw, 8, 18, "other", "other")
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := OpenWorkspaceRunSettingsDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	empty, err := database.LoadWorkspaceRunSettings(context.Background(), 17)
	if err != nil || empty.WorkspaceID != 17 || empty.Version != "" || !reflect.DeepEqual(empty.Settings, storage.CodexRunSettings{}) {
		t.Fatalf("empty settings = (%#v, %v)", empty, err)
	}
	want := storage.CodexRunSettings{
		Executable: "/opt/tools/codex", Model: "future-model", ReasoningEffort: "high",
		Limits: storage.CodexRunLimits{MaxMessageBytes: 4096, EventBuffer: 8, MaxPending: 9, StderrBytes: 1024},
	}
	saved, err := database.SaveWorkspaceRunSettings(context.Background(), storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: 17, Settings: want})
	if err != nil || saved.Version == "" || saved.Revision != 1 || !reflect.DeepEqual(saved.Settings, want) || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved settings = (%#v, %v)", saved, err)
	}
	idempotent, err := database.SaveWorkspaceRunSettings(context.Background(), storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: 17, Settings: want, ExpectedVersion: saved.Version})
	if err != nil || !reflect.DeepEqual(idempotent, saved) {
		t.Fatalf("idempotent settings = (%#v, %v), want %#v", idempotent, err, saved)
	}
	changed := want
	changed.Model = "another-model"
	if _, err := database.SaveWorkspaceRunSettings(context.Background(), storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: 17, Settings: changed}); err == nil {
		t.Fatal("stale save succeeded")
	} else {
		var conflict *storage.WorkspaceRunSettingsConflict
		if !errors.As(err, &conflict) || !reflect.DeepEqual(conflict.Current, saved) {
			t.Fatalf("stale save error = %#v", err)
		}
	}
	loaded, err := database.LoadWorkspaceRunSettings(context.Background(), 17)
	if err != nil || !reflect.DeepEqual(loaded, saved) {
		t.Fatalf("stale save changed settings to %#v, %v", loaded, err)
	}
	updated, err := database.SaveWorkspaceRunSettings(context.Background(), storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: 17, Settings: changed, ExpectedVersion: saved.Version})
	if err != nil || updated.Version == saved.Version || updated.Revision != 2 || !reflect.DeepEqual(updated.Settings, changed) {
		t.Fatalf("updated settings = (%#v, %v)", updated, err)
	}

	if _, err := database.db.Exec(`UPDATE projects SET code = 'after' WHERE project_id = 7`); err != nil {
		t.Fatal(err)
	}
	afterRename, err := database.LoadWorkspaceRunSettings(context.Background(), 17)
	if err != nil || !reflect.DeepEqual(afterRename, updated) {
		t.Fatalf("project rename changed workspace settings to %#v, %v", afterRename, err)
	}
	other, err := database.LoadWorkspaceRunSettings(context.Background(), 18)
	if err != nil || other.Version != "" || !reflect.DeepEqual(other.Settings, storage.CodexRunSettings{}) {
		t.Fatalf("settings crossed workspace identity: %#v, %v", other, err)
	}
}

func TestWorkspaceRunSettingsRejectsUnknownWorkspaceAndInvalidStoredLimits(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "invalid-settings.db")
	database, err := OpenWorkspaceRunSettingsDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.LoadWorkspaceRunSettings(context.Background(), 99); err == nil || domain.PublicError(err).Code != "workspace_not_registered" {
		t.Fatalf("unknown workspace load = %v", err)
	}
	if _, err := database.SaveWorkspaceRunSettings(context.Background(), storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: 99}); err == nil || domain.PublicError(err).Code != "workspace_not_registered" {
		t.Fatalf("unknown workspace save = %v", err)
	}
	raw := database.db
	mustInsertProjectAndWorkspace(t, raw, 1, 1, "demo", "repo")
	if _, err := database.SaveWorkspaceRunSettings(context.Background(), storage.SaveWorkspaceRunSettingsRequest{
		WorkspaceID: 1, Settings: storage.CodexRunSettings{Limits: storage.CodexRunLimits{MaxMessageBytes: 1}},
	}); err == nil || domain.PublicError(err).Code != "workspace_run_settings_storage_failed" {
		t.Fatalf("invalid limit save = %v", err)
	}
}
