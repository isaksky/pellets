package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestWorkspaceRunSettingsManagerSavesAndLoadsRegisteredWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.db")
	projects, err := sqlite.OpenProjectDatabase(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := projects.RegisterProject(ctx, storage.ProjectRegistration{
		Code: "demo", GitCommonDir: domain.LocalPath{Value: "repo/.git", Relative: true},
		WorkspaceRoot: domain.LocalPath{Value: "repo", Relative: true}, GitDir: domain.LocalPath{Value: "repo/.git", Relative: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := projects.Close(); err != nil {
		t.Fatal(err)
	}
	workspaceID := project.Workspaces[0].ID
	manager := WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
		return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
	}}
	want := storage.CodexRunSettings{Model: "gpt-5.6-sol", ReasoningEffort: "high"}
	saved, err := manager.Save(ctx, Database{Path: path}, storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: workspaceID, Settings: want})
	if err != nil || saved.Version == "" || saved.Revision != 1 || !reflect.DeepEqual(saved.Settings, want) {
		t.Fatalf("Save = (%#v, %v)", saved, err)
	}
	loaded, err := manager.Load(ctx, Database{Path: path}, workspaceID)
	if err != nil || !reflect.DeepEqual(loaded, saved) {
		t.Fatalf("Load = (%#v, %v), want %#v", loaded, err, saved)
	}
}

func TestWorkspaceRunSettingsManagerValidatesBeforeOpeningStorage(t *testing.T) {
	t.Parallel()
	opened := false
	manager := WorkspaceRunSettingsManager{Open: func(context.Context, string) (storage.WorkspaceRunSettingsDatabase, error) {
		opened = true
		return nil, errors.New("must not open")
	}}
	_, err := manager.Save(context.Background(), Database{}, storage.SaveWorkspaceRunSettingsRequest{
		WorkspaceID: 1, Settings: storage.CodexRunSettings{Model: "bad\nmodel"},
	})
	if err == nil || domain.PublicError(err).Code != "invalid_workspace_run_settings" || opened {
		t.Fatalf("invalid Save = %v, opened = %v", err, opened)
	}
	if _, err := (WorkspaceRunSettingsManager{}).Load(context.Background(), Database{}, 1); err == nil || domain.PublicError(err).Code != "internal_error" {
		t.Fatalf("unconfigured Load = %v", err)
	}
}
