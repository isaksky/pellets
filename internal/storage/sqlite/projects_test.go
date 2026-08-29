package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestProjectRegistrationSharesRepositoryAcrossWorkspaces(t *testing.T) {
	t.Parallel()

	database, err := OpenProjectDatabase(context.Background(), filepath.Join(t.TempDir(), "projects.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	main := projectRegistration("foo", "repo/.git", "repo", "repo/.git")
	project, created, err := database.RegisterProject(context.Background(), main)
	if err != nil || !created || project.Code != "foo" || len(project.Workspaces) != 1 {
		t.Fatalf("main registration = (%#v, %v, %v)", project, created, err)
	}
	firstSnapshot := project

	project, created, err = database.RegisterProject(context.Background(), main)
	if err != nil || created || !reflect.DeepEqual(project, firstSnapshot) {
		t.Fatalf("idempotent registration = (%#v, %v, %v), want %#v", project, created, err, firstSnapshot)
	}

	linked := projectRegistration("foo", "repo/.git", "linked one", "repo/.git/worktrees/one")
	project, created, err = database.RegisterProject(context.Background(), linked)
	if err != nil || !created || len(project.Workspaces) != 2 || project.ID != firstSnapshot.ID {
		t.Fatalf("linked registration = (%#v, %v, %v)", project, created, err)
	}
	resolved, err := database.ResolveProjectWorkspace(context.Background(), linked.GitCommonDir, linked.WorkspaceRoot, linked.GitDir)
	if err != nil || resolved.Project.ID != project.ID || resolved.Workspace.RootPath != linked.WorkspaceRoot {
		t.Fatalf("resolved linked workspace = (%#v, %v)", resolved, err)
	}

	projects, err := database.ListProjects(context.Background())
	if err != nil || len(projects) != 1 || len(projects[0].Workspaces) != 2 {
		t.Fatalf("projects = (%#v, %v)", projects, err)
	}
}

func TestProjectRegistrationConflictsAreWriteFree(t *testing.T) {
	t.Parallel()

	database, err := OpenProjectDatabase(context.Background(), filepath.Join(t.TempDir(), "conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	main := projectRegistration("foo", "repo/.git", "repo", "repo/.git")
	if _, _, err := database.RegisterProject(context.Background(), main); err != nil {
		t.Fatal(err)
	}
	other := projectRegistration("bar", "other/.git", "other", "other/.git")
	if _, _, err := database.RegisterProject(context.Background(), other); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		registration storage.ProjectRegistration
		code         string
	}{
		{name: "same repository different code", registration: projectRegistration("changed", "repo/.git", "linked", "repo/.git/worktrees/linked"), code: "project_repository_already_registered"},
		{name: "same code unrelated repository", registration: projectRegistration("foo", "third/.git", "third", "third/.git"), code: "project_code_already_registered"},
		{name: "root already belongs elsewhere", registration: projectRegistration("foo", "repo/.git", "other", "repo/.git/worktrees/new"), code: "workspace_identity_conflict"},
		{name: "Git directory disagrees with root", registration: projectRegistration("foo", "repo/.git", "new", "other/.git"), code: "workspace_identity_conflict"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := projectCounts(t, database.db)
			_, _, err := database.RegisterProject(context.Background(), test.registration)
			if err == nil || domain.PublicError(err).Code != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
			if after := projectCounts(t, database.db); after != before {
				t.Fatalf("row counts changed from %v to %v", before, after)
			}
		})
	}
}

func TestWorkspaceMoveRequiresExplicitPreflightOverride(t *testing.T) {
	t.Parallel()

	database, err := OpenProjectDatabase(context.Background(), filepath.Join(t.TempDir(), "move.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	registration := projectRegistration("foo", "repo/.git", "old", "repo/.git/worktrees/topic")
	project, _, err := database.RegisterProject(context.Background(), registration)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := project.Workspaces[0].ID

	moved := registration
	moved.WorkspaceRoot = relativeProjectPath("new")
	if _, _, err := database.RegisterProject(context.Background(), moved); err == nil || domain.PublicError(err).Code != "workspace_identity_conflict" {
		t.Fatalf("unconfirmed move error = %v", err)
	}
	moved.AllowWorkspaceMove = true
	project, created, err := database.RegisterProject(context.Background(), moved)
	if err != nil || created || len(project.Workspaces) != 1 || project.Workspaces[0].ID != workspaceID || project.Workspaces[0].RootPath != moved.WorkspaceRoot {
		t.Fatalf("confirmed move = (%#v, %v, %v)", project, created, err)
	}
}

func TestResolveUnknownWorkspaceDoesNotRegisterIt(t *testing.T) {
	t.Parallel()
	database, err := OpenProjectDatabase(context.Background(), filepath.Join(t.TempDir(), "resolve.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	registration := projectRegistration("foo", "repo/.git", "repo", "repo/.git")
	if _, _, err := database.RegisterProject(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	before := projectCounts(t, database.db)
	_, err = database.ResolveProjectWorkspace(context.Background(), registration.GitCommonDir, relativeProjectPath("unregistered"), relativeProjectPath("repo/.git/worktrees/unregistered"))
	if err == nil || domain.PublicError(err).Code != "workspace_not_registered" {
		t.Fatalf("ResolveProjectWorkspace error = %v", err)
	}
	if after := projectCounts(t, database.db); after != before {
		t.Fatalf("read-only resolution changed rows from %v to %v", before, after)
	}
}

func projectRegistration(code, common, root, gitDir string) storage.ProjectRegistration {
	return storage.ProjectRegistration{Code: code, GitCommonDir: relativeProjectPath(common), WorkspaceRoot: relativeProjectPath(root), GitDir: relativeProjectPath(gitDir)}
}

func relativeProjectPath(value string) domain.LocalPath {
	return domain.LocalPath{Value: value, Relative: true}
}

func projectCounts(t *testing.T, database interface{ QueryRow(string, ...any) *sql.Row }) [2]int {
	t.Helper()
	var counts [2]int
	if err := database.QueryRow("SELECT (SELECT COUNT(*) FROM projects), (SELECT COUNT(*) FROM project_workspaces)").Scan(&counts[0], &counts[1]); err != nil {
		t.Fatal(err)
	}
	return counts
}
