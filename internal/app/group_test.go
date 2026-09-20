package app

import (
	"context"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestGroupManagerKeepsCurrentWorktreeProjectSelection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "groups.db")
	db, err := sqlite.OpenProjectDatabase(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := db.RegisterProject(ctx, storage.ProjectRegistration{Code: "canonical", GitCommonDir: relative("repo/.git"), WorkspaceRoot: relative("repo"), GitDir: relative("repo/.git")})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	resolved := storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}
	fake := &fakeProjectDatabase{resolved: resolved, projectsByCode: map[string]storage.Project{"former": project}}
	manager := GroupManager{Projects: successfulProjectManager(fake), Open: func(ctx context.Context, path string) (storage.GroupRepository, error) {
		return sqlite.OpenGroupRepository(ctx, path)
	}}
	database := Database{Root: "/database", Path: path}
	g, err := manager.Create(ctx, database, "/working", "former", "empty")
	if err != nil || g.ProjectID != project.ID {
		t.Fatalf("create: %+v %v", g, err)
	}
	g, err = manager.EditContext(ctx, database, "/working", "", g.ID, g.Revision, "# Context")
	if err != nil {
		t.Fatal(err)
	}
	g, err = manager.Rename(ctx, database, "/working", "canonical", g.ID, g.Revision, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	got, err := manager.Read(ctx, database, "/working", "", g.ID)
	if err != nil || got != g {
		t.Fatalf("read: %+v %v", got, err)
	}
	list, err := manager.List(ctx, database, "/working", "former")
	if err != nil || len(list) != 1 || list[0] != g {
		t.Fatalf("list: %+v %v", list, err)
	}
	_, err = manager.Create(ctx, database, "/working", "foreign", "wrong project")
	if err == nil || domain.PublicError(err).Code != "project_selection_mismatch" {
		t.Fatalf("foreign selection: %v", err)
	}
}
