package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

const workspaceRunSettingsColumns = `workspace_id, codex_executable, model, reasoning_effort,
	max_message_bytes, event_buffer, max_pending, stderr_bytes, revision,
	strftime('%Y-%m-%dT%H:%M:%fZ', created_at),
	strftime('%Y-%m-%dT%H:%M:%fZ', updated_at)`

func OpenWorkspaceRunSettingsDatabase(ctx context.Context, databasePath string) (*ProjectDatabase, error) {
	return OpenProjectDatabase(ctx, databasePath)
}

func (database *ProjectDatabase) LoadWorkspaceRunSettings(ctx context.Context, workspaceID int64) (storage.SavedWorkspaceRunSettings, error) {
	if workspaceID < 1 {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsInputError("workspace ID must be positive")
	}
	settings, err := loadWorkspaceRunSettings(ctx, database.db, workspaceID)
	if err == nil {
		return settings, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("load settings", err)
	}
	var exists bool
	if err := database.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM project_workspaces WHERE workspace_id = ?)", workspaceID).Scan(&exists); err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("find workspace", err)
	}
	if !exists {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsNotFound(workspaceID)
	}
	return storage.SavedWorkspaceRunSettings{WorkspaceID: workspaceID}, nil
}

func (database *ProjectDatabase) SaveWorkspaceRunSettings(ctx context.Context, request storage.SaveWorkspaceRunSettingsRequest) (storage.SavedWorkspaceRunSettings, error) {
	if request.WorkspaceID < 1 {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsInputError("workspace ID must be positive")
	}
	connection, err := database.db.Conn(ctx)
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("open save connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("begin save", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	current, loadErr := loadWorkspaceRunSettings(ctx, connection, request.WorkspaceID)
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("read current settings", loadErr)
	}
	if errors.Is(loadErr, sql.ErrNoRows) {
		var exists bool
		if err := connection.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM project_workspaces WHERE workspace_id = ?)", request.WorkspaceID).Scan(&exists); err != nil {
			return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("find workspace", err)
		}
		if !exists {
			return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsNotFound(request.WorkspaceID)
		}
		current = storage.SavedWorkspaceRunSettings{WorkspaceID: request.WorkspaceID}
	}
	if current.Version != request.ExpectedVersion {
		return storage.SavedWorkspaceRunSettings{}, &storage.WorkspaceRunSettingsConflict{Current: current}
	}
	if current.Revision > 0 && reflect.DeepEqual(current.Settings, request.Settings) {
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("commit idempotent save", err)
		}
		committed = true
		return current, nil
	}

	var timestamp float64
	if err := connection.QueryRowContext(ctx, "SELECT julianday('now')").Scan(&timestamp); err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("capture save time", err)
	}
	nextRevision := current.Revision + 1
	settings := request.Settings
	if current.Revision == 0 {
		_, err = connection.ExecContext(ctx, `
			INSERT INTO workspace_run_settings(
				workspace_id, codex_executable, model, reasoning_effort,
				max_message_bytes, event_buffer, max_pending, stderr_bytes,
				revision, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			request.WorkspaceID, settings.Executable, settings.Model, settings.ReasoningEffort,
			settings.Limits.MaxMessageBytes, settings.Limits.EventBuffer, settings.Limits.MaxPending, settings.Limits.StderrBytes,
			nextRevision, timestamp, timestamp)
	} else {
		_, err = connection.ExecContext(ctx, `
			UPDATE workspace_run_settings
			SET codex_executable = ?, model = ?, reasoning_effort = ?,
				max_message_bytes = ?, event_buffer = ?, max_pending = ?, stderr_bytes = ?,
				revision = ?, updated_at = ?
			WHERE workspace_id = ?`,
			settings.Executable, settings.Model, settings.ReasoningEffort,
			settings.Limits.MaxMessageBytes, settings.Limits.EventBuffer, settings.Limits.MaxPending, settings.Limits.StderrBytes,
			nextRevision, timestamp, request.WorkspaceID)
	}
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("write settings", err)
	}
	saved, err := loadWorkspaceRunSettings(ctx, connection, request.WorkspaceID)
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("read saved settings", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.SavedWorkspaceRunSettings{}, workspaceRunSettingsStorageError("commit save", err)
	}
	committed = true
	return saved, nil
}

type workspaceRunSettingsQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadWorkspaceRunSettings(ctx context.Context, query workspaceRunSettingsQuery, workspaceID int64) (storage.SavedWorkspaceRunSettings, error) {
	row := query.QueryRowContext(ctx, "SELECT "+workspaceRunSettingsColumns+" FROM workspace_run_settings WHERE workspace_id = ?", workspaceID)
	var saved storage.SavedWorkspaceRunSettings
	var createdAt, updatedAt string
	err := row.Scan(
		&saved.WorkspaceID, &saved.Settings.Executable, &saved.Settings.Model, &saved.Settings.ReasoningEffort,
		&saved.Settings.Limits.MaxMessageBytes, &saved.Settings.Limits.EventBuffer,
		&saved.Settings.Limits.MaxPending, &saved.Settings.Limits.StderrBytes,
		&saved.Revision, &createdAt, &updatedAt,
	)
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, err
	}
	saved.CreatedAt, err = parseProjectTimestamp("workspace run settings created_at", createdAt)
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, err
	}
	saved.UpdatedAt, err = parseProjectTimestamp("workspace run settings updated_at", updatedAt)
	saved.Version = storage.WorkspaceRunSettingsVersion(saved)
	return saved, err
}

func workspaceRunSettingsNotFound(workspaceID int64) error {
	return domain.NewError(domain.NotFound, "workspace_not_registered", "the selected Git worktree is not registered in the Pellets database", map[string]any{"workspace_id": workspaceID})
}

func workspaceRunSettingsInputError(message string) error {
	return domain.NewError(domain.Usage, "invalid_workspace_run_settings", message, nil)
}

func workspaceRunSettingsStorageError(operation string, err error) error {
	if stable := stableDatabaseError(operation, err); stable != nil {
		return stable
	}
	return domain.WrapError(domain.Storage, "workspace_run_settings_storage_failed", "could not access workspace run settings", map[string]any{"operation": operation}, fmt.Errorf("%s: %w", operation, err))
}
