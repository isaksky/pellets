package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestPruneMissingWorkspaceProtections(t *testing.T) {
	for _, scenario := range []string{"missing", "present", "symlink", "parent_missing", "parent_missing_metadata", "unremoved_metadata", "repository_missing", "schedule", "recovery", "execution", "moved", "locked"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			database := Database{Root: root, Path: filepath.Join(root, "pellets.db")}
			db, err := sqlite.OpenProjectDatabase(ctx, database.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rel := func(s string) domain.LocalPath { return domain.LocalPath{Value: s, Relative: true} }
			common := filepath.Join(root, "repo/.git")
			if err := os.MkdirAll(common, 0700); err != nil {
				t.Fatal(err)
			}
			_, _, err = db.RegisterProject(ctx, storage.ProjectRegistration{Code: "demo", GitCommonDir: rel("repo/.git"), WorkspaceRoot: rel("repo"), GitDir: rel("repo/.git")})
			if err != nil {
				t.Fatal(err)
			}
			linked := "linked"
			if scenario == "parent_missing" || scenario == "parent_missing_metadata" {
				linked = "absent/linked"
			}
			project, _, err := db.RegisterProject(ctx, storage.ProjectRegistration{Code: "demo", GitCommonDir: rel("repo/.git"), WorkspaceRoot: rel(linked), GitDir: rel("repo/.git/worktrees/linked")})
			if err != nil {
				t.Fatal(err)
			}
			w := project.Workspaces[1]
			switch scenario {
			case "parent_missing_metadata", "unremoved_metadata":
				gitdir := filepath.Join(common, "worktrees/linked")
				if err := os.MkdirAll(gitdir, 0700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(gitdir, "gitdir"), []byte(filepath.Join(root, linked, ".git")+"\n"), 0600)
			case "locked":
				gitdir := filepath.Join(common, "worktrees/linked")
				if err := os.MkdirAll(gitdir, 0700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(gitdir, "locked"), []byte("offline"), 0600)
			case "moved":
				gitdir := filepath.Join(common, "worktrees/linked")
				if err := os.MkdirAll(gitdir, 0700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(gitdir, "gitdir"), []byte(filepath.Join(root, "elsewhere/.git")+"\n"), 0600)
			case "present":
				err = os.Mkdir(filepath.Join(root, linked), 0700)
			case "symlink":
				err = os.Symlink("absent", filepath.Join(root, linked))
			case "repository_missing":
				err = os.RemoveAll(common)
			case "schedule":
				var lock *executionlock.Lock
				lock, err = workspaceScheduleGuard(database, w.ID)
				if err == nil {
					defer lock.Close()
				}
			case "execution", "recovery":
				gitdir := filepath.Join(root, "repo/.git/worktrees/linked")
				err = os.MkdirAll(gitdir, 0700)
				if err != nil {
					t.Fatal(err)
				}
				lock, e := executionlock.Acquire(gitdir)
				if e != nil {
					t.Fatal(e)
				}
				if scenario == "execution" {
					defer lock.Close()
				} else {
					err = lock.Record(executionlock.Owner{Database: database.Path, RunID: 1})
					lock.Close()
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			dry, err := PruneWorkspaces(ctx, database, project, db, true)
			if err != nil {
				t.Fatal(err)
			}
			expected := 0
			if scenario == "missing" || scenario == "parent_missing" || scenario == "unremoved_metadata" {
				expected = 1
			}
			if len(dry.Pruned) != expected {
				t.Fatalf("dry run: %+v", dry)
			}
			if scenario == "missing" || scenario == "parent_missing" || scenario == "unremoved_metadata" {
				if _, statErr := os.Stat(filepath.Join(root, "workspace-guards")); !os.IsNotExist(statErr) {
					t.Fatalf("dry run created guard files: %v", statErr)
				}
			}
			current, err := db.FindProjectByCode(ctx, "demo")
			if err != nil || len(current.Workspaces) != 2 {
				t.Fatalf("dry run mutated registration: %+v %v", current, err)
			}
			result, err := PruneWorkspaces(ctx, database, project, db, false)
			if err != nil || len(result.Pruned) != expected {
				t.Fatalf("prune: %+v %v", result, err)
			}
			current, err = db.FindProjectByCode(ctx, "demo")
			if err != nil || len(current.Workspaces) != 2-expected {
				t.Fatalf("registration: %+v %v", current, err)
			}
		})
	}
}

func TestPruneProtectsWaitingSchedule(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "success")
	ctx := context.Background()
	db, err := sqlite.OpenProjectDatabase(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	linked := filepath.Join(t.TempDir(), "missing-agent")
	gitForExecutionTest(t, s.options.Database.Root, "worktree", "add", "--detach", linked)
	identity, err := discovery.FindGitIdentity(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	rootPath, err := discovery.NormalizeLocalPath(s.options.Database.Root, identity.WorkTreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := discovery.NormalizeLocalPath(s.options.Database.Root, identity.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := db.RegisterProject(ctx, storage.ProjectRegistration{Code: request.Selected.Project.Code, GitCommonDir: request.Selected.Project.GitCommonDir, WorkspaceRoot: rootPath, GitDir: gitDir})
	if err != nil {
		t.Fatal(err)
	}
	request.Selected = storage.ResolvedProject{Project: p, Workspace: p.Workspaces[len(p.Workspaces)-1]}
	filter := "no-matching-work"
	request.ExternalID = &filter
	request.Mode = "watch"
	handle := startSchedule(t, s, request)
	awaitScheduleState(t, handle, "waiting")
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	result, err := PruneWorkspaces(ctx, s.options.Database, p, db, false)
	if err != nil || len(result.Pruned) != 0 {
		t.Fatalf("waiting schedule pruned: %+v %v", result, err)
	}
	handle.StopNow()
	awaitSchedule(t, handle)
	result, err = PruneWorkspaces(ctx, s.options.Database, p, db, false)
	if err != nil || len(result.Pruned) != 1 {
		t.Fatalf("stopped schedule protected: %+v %v", result, err)
	}
}
