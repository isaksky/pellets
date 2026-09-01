package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sync"
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

func TestAutomaticProjectCodesReuseAndResolveCollisionsTransactionally(t *testing.T) {
	t.Parallel()

	database, err := OpenProjectDatabase(context.Background(), filepath.Join(t.TempDir(), "automatic-codes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	first := automaticProjectRegistration("Service API", "first/.git", "first", "first/.git")
	firstProject, created, err := database.RegisterProject(context.Background(), first)
	if err != nil || !created || firstProject.Code != "service-api" {
		t.Fatalf("first automatic registration = (%#v, %v, %v)", firstProject, created, err)
	}

	linked := first
	linked.CodeName = "a linked checkout name must not replace the stored code"
	linked.WorkspaceRoot = relativeProjectPath("first-linked")
	linked.GitDir = relativeProjectPath("first/.git/worktrees/linked")
	linkedProject, created, err := database.RegisterProject(context.Background(), linked)
	if err != nil || !created || linkedProject.Code != firstProject.Code || len(linkedProject.Workspaces) != 2 {
		t.Fatalf("linked automatic registration = (%#v, %v, %v)", linkedProject, created, err)
	}

	second := automaticProjectRegistration("Service API", "second/.git", "second", "second/.git")
	secondProject, created, err := database.RegisterProject(context.Background(), second)
	if err != nil || !created || secondProject.Code == firstProject.Code {
		t.Fatalf("colliding automatic registration = (%#v, %v, %v)", secondProject, created, err)
	}
	if err := domain.ValidateProjectCode(secondProject.Code); err != nil {
		t.Fatalf("collision code %q is invalid: %v", secondProject.Code, err)
	}
	repeated, created, err := database.RegisterProject(context.Background(), second)
	if err != nil || created || repeated.Code != secondProject.Code {
		t.Fatalf("repeated collision registration = (%#v, %v, %v), want stable code %q", repeated, created, err, secondProject.Code)
	}

	empty := automaticProjectRegistration("界", "unicode/.git", "unicode", "unicode/.git")
	emptyProject, _, err := database.RegisterProject(context.Background(), empty)
	if err != nil || domain.ValidateProjectCode(emptyProject.Code) != nil || emptyProject.Code[:2] != "p-" {
		t.Fatalf("empty normalized name registration = (%#v, %v)", emptyProject, err)
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

func TestProjectRenamePreservesStableIdentityAndCanonicalizesRedirectReferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rename.db")
	database, err := OpenProjectDatabase(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := database.RegisterProject(ctx, projectRegistration("foo", "repo/.git", "repo", "repo/.git"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenPelletRepository(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	resolved := storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}
	pellet, err := repository.CreatePellet(ctx, resolved, storage.NewPellet{Title: "stable number"})
	if err != nil {
		t.Fatal(err)
	}

	plan, err := database.PlanProjectRename(ctx, project.ID, "bar")
	if err != nil || len(plan.Conflicts) != 0 {
		t.Fatalf("rename plan = (%#v, %v)", plan, err)
	}
	result, err := database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: project.ID, NewCode: "bar"})
	if err != nil || !result.Changed || result.Project.ID != project.ID || result.Project.Code != "bar" || result.PreviousCode != "foo" {
		t.Fatalf("rename result = (%#v, %v)", result, err)
	}
	if len(result.Project.Redirects) != 1 || result.Project.Redirects[0].Code != "foo" || result.Project.Redirects[0].ProjectID != project.ID {
		t.Fatalf("renamed redirects = %#v", result.Project.Redirects)
	}
	for _, code := range []string{"foo", "bar"} {
		resolvedProject, err := database.FindProjectByCode(ctx, code)
		if err != nil || resolvedProject.ID != project.ID || resolvedProject.Code != "bar" {
			t.Fatalf("FindProjectByCode(%q) = (%#v, %v)", code, resolvedProject, err)
		}
	}
	staleContext := resolved
	canonical, err := repository.ReadPellet(ctx, staleContext, domain.PelletReference{ProjectCode: "foo", Number: pellet.Reference.Number})
	if err != nil || canonical.ProjectID != project.ID || canonical.Reference.String() != "bar-1" {
		t.Fatalf("old reference read = (%#v, %v)", canonical, err)
	}
	if pellet.Reference.Number != canonical.Reference.Number {
		t.Fatalf("pellet number changed from %d to %d", pellet.Reference.Number, canonical.Reference.Number)
	}

	idempotent, err := database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: project.ID, NewCode: "bar"})
	if err != nil || idempotent.Changed || !reflect.DeepEqual(idempotent.Project, result.Project) {
		t.Fatalf("idempotent rename = (%#v, %v), want unchanged %#v", idempotent, err, result.Project)
	}
	closed, err := repository.TransitionPellet(ctx, staleContext, domain.PelletReference{ProjectCode: "foo", Number: 1}, storage.PelletLifecycleRequest{Operation: storage.PelletClose})
	if err != nil || closed.Pellet.Reference.String() != "bar-1" {
		t.Fatalf("close through old reference = (%#v, %v)", closed, err)
	}
	// A repository retained across the rename may still hold the old project
	// snapshot. Stable identity accepts it, but every successful result must use
	// the current canonical code.
	purged, err := repository.PurgeClosedPellets(ctx, project, storage.PelletPurgeOptions{})
	if err != nil || len(purged) != 1 || purged[0].String() != "bar-1" {
		t.Fatalf("purge after rename = (%#v, %v)", purged, err)
	}
	if redirected, err := database.FindProjectByCode(ctx, "foo"); err != nil || redirected.ID != project.ID || redirected.Code != "bar" {
		t.Fatalf("purge changed project redirect = (%#v, %v)", redirected, err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectRenamePromotionCanonicalCollisionAndRedirectConfirmation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := OpenProjectDatabase(ctx, filepath.Join(t.TempDir(), "rename-conflicts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	foo, _, err := database.RegisterProject(ctx, projectRegistration("foo", "foo/.git", "foo", "foo/.git"))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := database.RegisterProject(ctx, projectRegistration("other", "other/.git", "other", "other/.git"))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := database.PlanProjectRename(ctx, other.ID, "baz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: other.ID, NewCode: "baz"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PlanProjectRename(ctx, foo.ID, "baz"); err == nil || domain.PublicError(err).Code != "project_code_already_registered" {
		t.Fatalf("canonical collision error = %v", err)
	}

	conflictPlan, err := database.PlanProjectRename(ctx, foo.ID, "other")
	if err != nil || len(conflictPlan.Conflicts) != 1 || conflictPlan.Conflicts[0].CanonicalCode != "baz" {
		t.Fatalf("redirect conflict plan = (%#v, %v)", conflictPlan, err)
	}
	before := projectNamespaceState(t, database.db)
	_, err = database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: foo.ID, NewCode: "other"})
	if err == nil || domain.PublicError(err).Code != "project_rename_confirmation_required" {
		t.Fatalf("unconfirmed redirect rename error = %v", err)
	}
	if after := projectNamespaceState(t, database.db); after != before {
		t.Fatalf("unconfirmed rename changed namespace from %q to %q", before, after)
	}
	confirmed, err := database.RenameProject(ctx, storage.ProjectRenameRequest{
		ProjectID: foo.ID, NewCode: "other", DeleteConflictingRedirects: true,
		ExpectedConflictingRedirects: conflictPlan.Conflicts,
	})
	if err != nil || confirmed.Project.Code != "other" || len(confirmed.RemovedConflicts) != 1 {
		t.Fatalf("confirmed redirect rename = (%#v, %v)", confirmed, err)
	}
	resolvedOther, err := database.FindProjectByCode(ctx, "other")
	if err != nil || resolvedOther.ID != foo.ID || resolvedOther.Code != "other" {
		t.Fatalf("promoted conflicting code = (%#v, %v)", resolvedOther, err)
	}

	// Promoting the project's own former code is safe and reverses the
	// canonical/redirect pair without a destructive confirmation.
	promotionPlan, err := database.PlanProjectRename(ctx, foo.ID, "foo")
	if err != nil || len(promotionPlan.Conflicts) != 0 {
		t.Fatalf("own redirect promotion plan = (%#v, %v)", promotionPlan, err)
	}
	promoted, err := database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: foo.ID, NewCode: "foo"})
	if err != nil || promoted.Project.Code != "foo" || len(promoted.Project.Redirects) != 1 || promoted.Project.Redirects[0].Code != "other" {
		t.Fatalf("own redirect promotion = (%#v, %v)", promoted, err)
	}
	_ = plan
}

func TestProjectRenameRevalidatesConflictSetAndRollsBackInjectedFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := OpenProjectDatabase(ctx, filepath.Join(t.TempDir(), "rename-revalidate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	foo, _, _ := database.RegisterProject(ctx, projectRegistration("foo", "foo/.git", "foo", "foo/.git"))
	first, _, _ := database.RegisterProject(ctx, projectRegistration("first", "first/.git", "first", "first/.git"))
	second, _, _ := database.RegisterProject(ctx, projectRegistration("second", "second/.git", "second", "second/.git"))
	if _, err := database.db.Exec(`INSERT INTO project_code_redirects(code, project_id, created_at, updated_at) VALUES ('target', ?, 1, 1)`, first.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := database.PlanProjectRename(ctx, foo.ID, "target")
	if err != nil || len(plan.Conflicts) != 1 {
		t.Fatalf("initial conflict plan = (%#v, %v)", plan, err)
	}
	if _, err := database.db.Exec(`UPDATE project_code_redirects SET project_id = ?, updated_at = 2 WHERE code = 'target'`, second.ID); err != nil {
		t.Fatal(err)
	}
	before := projectNamespaceState(t, database.db)
	_, err = database.RenameProject(ctx, storage.ProjectRenameRequest{
		ProjectID: foo.ID, NewCode: "target", DeleteConflictingRedirects: true,
		ExpectedConflictingRedirects: plan.Conflicts,
	})
	if err == nil || domain.PublicError(err).Code != "project_redirect_conflicts_changed" {
		t.Fatalf("stale conflict error = %v", err)
	}
	if after := projectNamespaceState(t, database.db); after != before {
		t.Fatalf("stale-conflict rename changed namespace from %q to %q", before, after)
	}

	if _, err := database.db.Exec(`
		CREATE TRIGGER inject_project_rename_failure
		BEFORE INSERT ON project_code_redirects
		WHEN NEW.code = 'foo'
		BEGIN SELECT RAISE(ABORT, 'injected rename failure'); END;`); err != nil {
		t.Fatal(err)
	}
	before = projectNamespaceState(t, database.db)
	_, err = database.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: foo.ID, NewCode: "fresh"})
	if err == nil {
		t.Fatal("injected project rename unexpectedly succeeded")
	}
	if after := projectNamespaceState(t, database.db); after != before {
		t.Fatalf("failed rename changed namespace from %q to %q", before, after)
	}
	if _, err := database.db.Exec(`DROP TRIGGER inject_project_rename_failure`); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCodeNamespaceRemainsUnambiguousUnderConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "namespace-concurrency.db")
	first, err := OpenProjectDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenProjectDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	foo, _, _ := first.RegisterProject(context.Background(), projectRegistration("foo", "foo/.git", "foo", "foo/.git"))
	bar, _, _ := first.RegisterProject(context.Background(), projectRegistration("bar", "bar/.git", "bar", "bar/.git"))

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	errorsFound := make(chan error, 2)
	go func() {
		defer wait.Done()
		<-start
		_, err := first.RenameProject(context.Background(), storage.ProjectRenameRequest{ProjectID: foo.ID, NewCode: "shared"})
		errorsFound <- err
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := second.db.Exec(`INSERT INTO project_code_redirects(code, project_id, created_at, updated_at) VALUES ('shared', ?, 1, 1)`, bar.ID)
		errorsFound <- err
	}()
	close(start)
	wait.Wait()
	close(errorsFound)
	successes := 0
	for err := range errorsFound {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent namespace writes had %d successes, want exactly 1", successes)
	}
	assertQueryInt(t, first.db, `
		SELECT (SELECT COUNT(*) FROM projects WHERE code = 'shared') +
		       (SELECT COUNT(*) FROM project_code_redirects WHERE code = 'shared')`, 1)
	assertQueryInt(t, first.db, `
		SELECT COUNT(*) FROM projects AS project
		JOIN project_code_redirects AS redirect ON redirect.code = project.code`, 0)
}

func projectNamespaceState(t *testing.T, database interface{ QueryRow(string, ...any) *sql.Row }) string {
	t.Helper()
	var state string
	err := database.QueryRow(`
		SELECT coalesce(group_concat(kind || ':' || code || ':' || project_id, ','), '')
		FROM (
			SELECT 'canonical' AS kind, code, project_id FROM projects
			UNION ALL
			SELECT 'redirect', code, project_id FROM project_code_redirects
			ORDER BY kind, code, project_id
		)`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func projectRegistration(code, common, root, gitDir string) storage.ProjectRegistration {
	return storage.ProjectRegistration{Code: code, GitCommonDir: relativeProjectPath(common), WorkspaceRoot: relativeProjectPath(root), GitDir: relativeProjectPath(gitDir)}
}

func automaticProjectRegistration(name, common, root, gitDir string) storage.ProjectRegistration {
	return storage.ProjectRegistration{
		CodeName: name, CodeIdentity: "true:" + common, GenerateCode: true,
		GitCommonDir: relativeProjectPath(common), WorkspaceRoot: relativeProjectPath(root), GitDir: relativeProjectPath(gitDir),
	}
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
