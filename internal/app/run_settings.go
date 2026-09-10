package app

import (
	"context"
	"errors"
	"fmt"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

type WorkspaceRunSettingsDatabaseOpener func(context.Context, string) (storage.WorkspaceRunSettingsDatabase, error)

// WorkspaceRunSettingsManager persists credential-free settings against one
// stable registered-workspace identity. Runtime account/configuration preflight
// remains in internal/codex and occurs only when a run is explicitly prepared.
type WorkspaceRunSettingsManager struct {
	Open WorkspaceRunSettingsDatabaseOpener
}

func (manager WorkspaceRunSettingsManager) Load(ctx context.Context, database Database, workspaceID int64) (storage.SavedWorkspaceRunSettings, error) {
	if manager.Open == nil {
		return storage.SavedWorkspaceRunSettings{}, runSettingsManagerConfigurationError()
	}
	if workspaceID < 1 {
		return storage.SavedWorkspaceRunSettings{}, invalidRunSettings("workspace ID must be positive", nil)
	}
	repository, err := manager.Open(ctx, database.Path)
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, err
	}
	settings, operationErr := repository.LoadWorkspaceRunSettings(ctx, workspaceID)
	return settings, closeWorkspaceRunSettingsDatabase(repository, operationErr)
}

func (manager WorkspaceRunSettingsManager) Save(ctx context.Context, database Database, request storage.SaveWorkspaceRunSettingsRequest) (storage.SavedWorkspaceRunSettings, error) {
	if manager.Open == nil {
		return storage.SavedWorkspaceRunSettings{}, runSettingsManagerConfigurationError()
	}
	if request.WorkspaceID < 1 {
		return storage.SavedWorkspaceRunSettings{}, invalidRunSettings("workspace ID must be positive", nil)
	}
	if len(request.Settings.Executable) > 4096 || len(request.Settings.Model) > 512 || len(request.Settings.ReasoningEffort) > 128 {
		return storage.SavedWorkspaceRunSettings{}, invalidRunSettings("an executable, model, or reasoning effort exceeds its storage limit", nil)
	}
	if _, err := codex.ResolveRunSettings(request.Settings, codex.RunOverrides{}); err != nil {
		return storage.SavedWorkspaceRunSettings{}, invalidRunSettings("workspace Codex run settings are invalid", err)
	}
	repository, err := manager.Open(ctx, database.Path)
	if err != nil {
		return storage.SavedWorkspaceRunSettings{}, err
	}
	saved, operationErr := repository.SaveWorkspaceRunSettings(ctx, request)
	return saved, closeWorkspaceRunSettingsDatabase(repository, operationErr)
}

func closeWorkspaceRunSettingsDatabase(database storage.WorkspaceRunSettingsDatabase, operationErr error) error {
	closeErr := database.Close()
	return errors.Join(operationErr, closeErr)
}

func invalidRunSettings(message string, err error) error {
	return domain.WrapError(domain.Usage, "invalid_workspace_run_settings", message, nil, err)
}

func runSettingsManagerConfigurationError() error {
	return domain.WrapError(domain.Unexpected, "internal_error", "workspace run settings manager is not configured", nil, fmt.Errorf("workspace run settings database opener is nil"))
}
