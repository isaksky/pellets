package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestPruneWorkspaceIdentityAndReRegistration(t *testing.T) {
	ctx := context.Background()
	db, err := OpenProjectDatabase(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := projectRegistration("demo", "repo/.git", "linked", "repo/.git/worktrees/linked")
	p, _, err := db.RegisterProject(ctx, reg)
	if err != nil {
		t.Fatal(err)
	}
	w := p.Workspaces[0]
	stale := w
	stale.RootPath.Value = "wrong"
	if reason, err := db.PruneWorkspace(ctx, stale, false); err != nil || reason != "workspace_changed" {
		t.Fatalf("stale: %s %v", reason, err)
	}
	if reason, err := db.PruneWorkspace(ctx, w, false); err != nil || reason != "" {
		t.Fatalf("prune: %s %v", reason, err)
	}
	assertQueryInt(t, db.db, "SELECT COUNT(*) FROM project_workspaces", 1)
	if _, err := db.ResolveProjectWorkspace(ctx, reg.GitCommonDir, reg.WorkspaceRoot, reg.GitDir); domain.PublicError(err).Code != "workspace_not_registered" {
		t.Fatalf("pruned resolved: %v", err)
	}
	if err := ensureStoredProjectWorkspace(ctx, db.db, storage.ResolvedProject{Project: p, Workspace: w}); err == nil {
		t.Fatal("stale registration accepted")
	}
	p, _, err = db.RegisterProject(ctx, reg)
	if err != nil || len(p.Workspaces) != 1 || p.Workspaces[0].ID != w.ID {
		t.Fatalf("re-registration: %+v %v", p, err)
	}
}
func TestPruneWorkspacePreservesRunHistoryAndBlocksOwnership(t *testing.T) {
	ctx := context.Background()
	db, run, f := createTestRun(t)
	w := f.main.Workspace
	if reason, err := db.PruneWorkspace(ctx, w, false); err != nil || reason != "work_or_recovery_pending" {
		t.Fatalf("active: %s %v", reason, err)
	}
	run = updateRun(t, db, run, storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "cancelled"}, "")
	q := f.open(t)
	defer q.Close()
	_, err := q.TransitionPellet(ctx, f.main, domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber}, storage.PelletLifecycleRequest{Operation: storage.PelletRelease})
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly released ownership leaves the stopped attempt as history.

	if reason, err := db.PruneWorkspace(ctx, w, false); err != nil || reason != "" {
		t.Fatalf("settled: %s %v", reason, err)
	}
	saved, err := db.ReadExecutionRun(ctx, run.ID)
	if err != nil || saved.WorkspaceID != w.ID || len(saved.Activity) == 0 {
		t.Fatalf("history lost: %+v %v", saved, err)
	}
}
