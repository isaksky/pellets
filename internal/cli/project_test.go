package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"pellets/internal/app"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/output"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestProjectWorkspaceJSONGolden(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	project := storage.Project{
		ID: 1, Code: "foo", GitCommonDir: domain.LocalPath{Value: "main/.git", Relative: true},
		Workspaces: []storage.Workspace{
			{ID: 1, ProjectID: 1, RootPath: domain.LocalPath{Value: "main", Relative: true}, GitDir: domain.LocalPath{Value: "main/.git", Relative: true}, CreatedAt: created, UpdatedAt: created},
			{ID: 2, ProjectID: 1, RootPath: domain.LocalPath{Value: "linked 界", Relative: true}, GitDir: domain.LocalPath{Value: "main/.git/worktrees/linked", Relative: true}, CreatedAt: updated, UpdatedAt: updated},
		},
		CreatedAt: created, UpdatedAt: updated,
	}
	var rendered bytes.Buffer
	if err := (output.JSONRenderer{}).Render(&rendered, "project show", newProjectData(project)); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "project-workspaces.golden", rendered.String())
}

func TestFirstCurrentProjectCommandCreatesDatabaseAtGitRoot(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "Git project with spaces and 世界")
	nested := filepath.Join(repository, "nested", "working directory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")

	current := nested
	application := projectTestApp(&current)
	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	initialized := decodeProjectResult(t, stdout, "project show")
	if err := domain.ValidateProjectCode(initialized.Code); err != nil || len(initialized.Workspaces) != 1 || initialized.Workspaces[0].RootPath != "." || initialized.GitCommonDir != ".git" {
		t.Fatalf("initialized project = %#v", initialized)
	}
	if _, err := os.Stat(discovery.DatabasePath(repository)); err != nil {
		t.Fatalf("database at Git root: %v", err)
	}
	if _, err := os.Stat(discovery.DatabasePath(nested)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database unexpectedly created at invocation directory: %v", err)
	}

	stdout, stderr, exit = runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	shown := decodeProjectResult(t, stdout, "project show")
	if !reflect.DeepEqual(shown, initialized) {
		t.Fatalf("current project = %#v, want %#v", shown, initialized)
	}

	stdout, stderr, exit = runTestApp(application, "project", "show", initialized.Code)
	if exit != 0 || stderr != "" {
		t.Fatalf("named project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if named := decodeProjectResult(t, stdout, "project show"); !reflect.DeepEqual(named, initialized) {
		t.Fatalf("named project = %#v, want %#v", named, initialized)
	}

	stdout, stderr, exit = runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("repeated project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if repeated := decodeProjectResult(t, stdout, "project show"); !reflect.DeepEqual(repeated, initialized) {
		t.Fatalf("idempotent project = %#v, want unchanged %#v", repeated, initialized)
	}

	projects := runProjectList(t, application)
	if !reflect.DeepEqual(projects, projectListData{initialized}) {
		t.Fatalf("project list = %#v, want one unchanged row", projects)
	}

}

func TestSiblingRepositoriesUseOneDatabaseWithUniqueCodes(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "common database 界 root")
	first := filepath.Join(common, "service alpha")
	second := filepath.Join(common, "service βeta")
	third := filepath.Join(common, "service duplicate code")
	for _, directory := range []string{first, second, third} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, directory, "init", "--quiet")
	}
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := first
	application := projectTestApp(&current)
	firstProject := runCurrentProject(t, application)
	if firstProject.Workspaces[0].RootPath != "service alpha" {
		t.Fatalf("first root path = %q", firstProject.Workspaces[0].RootPath)
	}
	current = second
	secondProject := runCurrentProject(t, application)
	if secondProject.Workspaces[0].RootPath != "service βeta" {
		t.Fatalf("second root path = %q", secondProject.Workspaces[0].RootPath)
	}

	current = third
	thirdProject := runCurrentProject(t, application)
	if firstProject.Code == secondProject.Code || firstProject.Code == thirdProject.Code || secondProject.Code == thirdProject.Code {
		t.Fatalf("automatically generated codes are not unique: %q %q %q", firstProject.Code, secondProject.Code, thirdProject.Code)
	}

	current = common
	projects := runProjectList(t, application)
	want := projectListData{firstProject, secondProject, thirdProject}
	sort.Slice(want, func(i, j int) bool { return want[i].Code < want[j].Code })
	if !reflect.DeepEqual(projects, want) {
		t.Fatalf("project list = %#v, want %#v", projects, want)
	}
	stdout, stderr, exit := runTestApp(application, "project", "show", secondProject.Code)
	if exit != 0 || stderr != "" {
		t.Fatalf("named show outside Git = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if shown := decodeProjectResult(t, stdout, "project show"); !reflect.DeepEqual(shown, secondProject) {
		t.Fatalf("shown project = %#v, want %#v", shown, secondProject)
	}
	stdout, stderr, exit = runTestApp(application, "--project", firstProject.Code, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("global named show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if shown := decodeProjectResult(t, stdout, "project show"); !reflect.DeepEqual(shown, firstProject) {
		t.Fatalf("globally selected project = %#v, want %#v", shown, firstProject)
	}
	stdout, stderr, exit = runTestApp(application, "project", "show")
	if exit != 3 || stdout != "" || !strings.Contains(stderr, `"code":"git_repository_not_found"`) {
		t.Fatalf("current show outside Git = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}

func TestProjectRenameCLIResolvesOldReferencesAndProtectsRedirectConflicts(t *testing.T) {
	common := filepath.Join(t.TempDir(), "rename CLI database")
	fooRoot := filepath.Join(common, "foo")
	otherRoot := filepath.Join(common, "other")
	thirdRoot := filepath.Join(common, "third")
	for _, directory := range []string{fooRoot, otherRoot, thirdRoot} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, directory, "init", "--quiet")
	}
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := fooRoot
	application := projectTestApp(&current)
	foo := runCurrentProject(t, application)
	if foo.Code != "foo" {
		t.Fatalf("foo bootstrap code = %q", foo.Code)
	}
	if stdout, stderr, exit := runTestApp(application, "add", "rename-stable pellet", "--external-id", "rename:stable", "--group", "redirect-group"); exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"foo-1"`) {
		t.Fatalf("pre-rename add = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	stdout, stderr, exit := runTestApp(application, "project", "rename", "bar")
	if exit != 0 || stderr != "" {
		t.Fatalf("rename foo to bar = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	renamed := decodeProjectRenameResult(t, stdout)
	if renamed.Status != "renamed" || renamed.PreviousCode != "foo" || renamed.Project.Code != "bar" || len(renamed.Project.Redirects) != 1 || renamed.Project.Redirects[0].Code != "foo" {
		t.Fatalf("rename result = %#v", renamed)
	}
	stdout, stderr, exit = runTestApp(application, "project", "show", "foo")
	if exit != 0 || stderr != "" || decodeProjectResult(t, stdout, "project show").Code != "bar" {
		t.Fatalf("old-code project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "show", "foo-1")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"bar-1"`) || strings.Contains(stdout, `"id":"foo-1"`) {
		t.Fatalf("old pellet reference show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "add", "placed through old reference", "--before", "foo-1")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"bar-2"`) {
		t.Fatalf("old placement reference = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current = otherRoot
	other := runCurrentProject(t, application)
	if other.Code != "other" {
		t.Fatalf("other bootstrap code = %q", other.Code)
	}
	if stdout, stderr, exit = runTestApp(application, "project", "rename", "baz"); exit != 0 || stderr != "" {
		t.Fatalf("rename other to baz = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	current = common
	application.stdin = errorReader{}
	stdout, stderr, exit = runTestApp(application, "--project", "bar", "project", "rename", "other")
	if exit != 6 || stdout != "" {
		t.Fatalf("non-interactive redirect conflict = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	assertCompactErrorCode(t, stderr, "project_rename_confirmation_required")
	if !strings.Contains(stderr, `"canonical_target":"baz"`) || !strings.Contains(stderr, `"retry_argv":["pl","--project","bar","project","rename","other","--delete-conflicting-redirects","--yes"]`) {
		t.Fatalf("confirmation contract = %q", stderr)
	}

	application.isInteractive = func(io.Reader, io.Writer) bool { return true }
	application.stdin = strings.NewReader("no\n")
	stdout, stderr, exit = runTestApp(application, "--human", "--project", "bar", "project", "rename", "other")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "other -> baz") || !strings.Contains(stdout, "[y/N]") || !strings.Contains(stdout, "cancelled") {
		t.Fatalf("interactive no = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	application.stdin = strings.NewReader("")
	stdout, stderr, exit = runTestApp(application, "--human", "--project", "bar", "project", "rename", "other")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "cancelled") {
		t.Fatalf("interactive EOF = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	application.stdin = errorReader{}
	stdout, stderr, exit = runTestApp(application, "--human", "--project", "bar", "project", "rename", "other")
	if exit != 1 || !strings.Contains(stderr, `"code":"internal_error"`) {
		t.Fatalf("interactive interruption = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "project", "show", "bar")
	if exit != 0 || stderr != "" || decodeProjectResult(t, stdout, "project show").Code != "bar" {
		t.Fatalf("interrupted rename changed project = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if stdout, stderr, exit = runTestApp(application, "--project", "bar", "project", "rename", "other", "--delete-conflicting-redirects", "--yes"); exit != 0 || stderr != "" {
		t.Fatalf("explicit redirect deletion = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	explicit := decodeProjectRenameResult(t, stdout)
	if explicit.Project.Code != "other" || len(explicit.RemovedRedirects) != 1 || explicit.RemovedRedirects[0].Code != "other" || explicit.RemovedRedirects[0].CanonicalTarget != "baz" {
		t.Fatalf("explicit rename result = %#v", explicit)
	}

	current = thirdRoot
	third := runCurrentProject(t, application)
	if third.Code != "third" {
		t.Fatalf("third bootstrap code = %q", third.Code)
	}
	if stdout, stderr, exit = runTestApp(application, "project", "rename", "qux"); exit != 0 || stderr != "" {
		t.Fatalf("rename third to qux = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	current = common
	application.stdin = strings.NewReader("yes\n")
	stdout, stderr, exit = runTestApp(application, "--human", "--project", "other", "project", "rename", "third")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "third -> qux") || !strings.Contains(stdout, "Renamed project other -> third") {
		t.Fatalf("interactive yes = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current = fooRoot
	stdout, stderr, exit = runTestApp(application, "--project", "foo", "memory", "add", "--text", "redirect memory")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"project":"third"`) {
		t.Fatalf("memory through old project selection = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	for label, args := range map[string][]string{
		"list filter":   {"--project", "foo", "list", "--external-id", "rename:stable", "--group", "redirect-group"},
		"search filter": {"--project", "bar", "search", "rename-stable", "--external-id", "rename:stable", "--group", "redirect-group"},
		"memory search": {"--project", "other", "memory", "search", "redirect"},
	} {
		stdout, stderr, exit = runTestApp(application, args...)
		if exit != 0 || stderr != "" || !strings.Contains(stdout, `"project":"third"`) {
			t.Fatalf("%s through redirected selection = exit %d stdout %q stderr %q", label, exit, stdout, stderr)
		}
	}
	stdout, stderr, exit = runTestApp(application, "--project", "foo", "move", "bar-2", "--after", "other-1")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"third-2"`) {
		t.Fatalf("move through old references = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "--project", "bar", "start", "foo-1")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"third-1"`) {
		t.Fatalf("start through old reference = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "--project", "other", "next", "--external-id", "rename:stable", "--group", "redirect-group")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"third-1"`) || !strings.Contains(stdout, `"selection_reason":"resume_in_progress"`) {
		t.Fatalf("next through old filter selection = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "--project", "foo", "close", "bar-1")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"third-1"`) {
		t.Fatalf("close through old reference = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "purge", "--project", "other", "--dry-run")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"project":"third"`) || !strings.Contains(stdout, `"third-1"`) {
		t.Fatalf("purge through old project selection = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "--project", "bar", "reopen", "other-1")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"third-1"`) {
		t.Fatalf("reopen through old reference = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	for _, reference := range []string{"foo-1", "bar-1", "other-1", "third-1"} {
		stdout, stderr, exit = runTestApp(application, "show", reference)
		if exit != 0 || stderr != "" || !strings.Contains(stdout, `"id":"third-1"`) {
			t.Fatalf("show %s = exit %d stdout %q stderr %q", reference, exit, stdout, stderr)
		}
	}
}

func TestRemovedInitCommandDoesNotCreateDatabase(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "code validation repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	current := repository
	application := projectTestApp(&current)

	stdout, stderr, exit := runTestApp(application, "init", "--code", "legacy")
	if exit != 2 || stdout != "" || !strings.Contains(stderr, `"code":"unknown_command"`) {
		t.Fatalf("removed init = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(repository, discovery.MetadataDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid code created metadata: %v", err)
	}
}

func TestBootstrapStoresAbsoluteIdentityWhenRepositoryIsOutsideDatabaseRoot(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "outer Git repository")
	databaseRoot := filepath.Join(repository, "nested database root")
	if err := os.MkdirAll(databaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	if stdout, stderr, exit := runTestApp(initDBTestApp(databaseRoot), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := databaseRoot
	application := projectTestApp(&current)
	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("outside bootstrap = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	project := decodeProjectResult(t, stdout, "project show")
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if project.GitCommonDirRelative || project.Workspaces[0].RootPathRelative || !strings.EqualFold(project.Workspaces[0].RootPath, filepath.ToSlash(canonicalRepository)) {
		t.Fatalf("outside project paths = %#v", project)
	}
}

func TestUnregisteredSiblingBootstrapsAndProjectValidationFailsCleanly(t *testing.T) {
	t.Parallel()

	common := t.TempDir()
	registered := filepath.Join(common, "registered")
	unregistered := filepath.Join(common, "unregistered")
	for _, directory := range []string{registered, unregistered} {
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, directory, "init", "--quiet")
	}
	if _, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 {
		t.Fatalf("init-db = exit %d stderr %q", exit, stderr)
	}
	current := registered
	application := projectTestApp(&current)
	registeredProject := runCurrentProject(t, application)
	current = unregistered

	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("unregistered sibling bootstrap = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if sibling := decodeProjectResult(t, stdout, "project show"); sibling.Code == registeredProject.Code {
		t.Fatalf("unrelated sibling reused code %q", sibling.Code)
	}

	tests := []struct {
		args []string
		code string
	}{
		{[]string{"project"}, "missing_subcommand"},
		{[]string{"project", "wat"}, "unknown_subcommand"},
		{[]string{"project", "list", "extra"}, "unexpected_argument"},
		{[]string{"project", "show", "known", "extra"}, "unexpected_argument"},
		{[]string{"--project", registeredProject.Code, "project", "list"}, "project_not_allowed"},
		{[]string{"--project", registeredProject.Code, "project", "show", registeredProject.Code}, "conflicting_project_selection"},
	}
	for _, test := range tests {
		stdout, stderr, exit = runTestApp(application, test.args...)
		if exit != 2 || stdout != "" || !strings.Contains(stderr, `"code":"`+test.code+`"`) {
			t.Errorf("%v = exit %d stdout %q stderr %q, want %s", test.args, exit, stdout, stderr, test.code)
		}
	}
}

func TestUsageValidationPrecedesWorkingDirectoryAndDiscovery(t *testing.T) {
	t.Parallel()

	manager := app.ProjectManager{}
	application := New(
		"test",
		InitDBCommand(app.DatabaseInitializer{}),
		ProjectCommand(manager),
	)
	workingDirectoryCalls := 0
	application.workingDirectory = func() (string, error) {
		workingDirectoryCalls++
		return "", errors.New("working directory boundary must not be crossed")
	}

	tests := []struct {
		name string
		args []string
		code string
	}{
		{name: "init-db project override", args: []string{"--project", "known", "init-db"}, code: "project_not_allowed"},
		{name: "project list override", args: []string{"--project", "known", "project", "list"}, code: "project_not_allowed"},
		{name: "conflicting show selection", args: []string{"--project", "known", "project", "show", "other"}, code: "conflicting_project_selection"},
		{name: "malformed global project", args: []string{"--project", "bad_code", "project", "show"}, code: "invalid_project_code"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, exit := runTestApp(application, test.args...)
			if exit != 2 || stdout != "" {
				t.Fatalf("%v = exit %d stdout %q stderr %q, want usage failure", test.args, exit, stdout, stderr)
			}
			assertCompactErrorCode(t, stderr, test.code)
		})
	}
	if workingDirectoryCalls != 0 {
		t.Fatalf("invalid invocations crossed working-directory boundary %d times", workingDirectoryCalls)
	}

	root := t.TempDir()
	application.workingDirectory = func() (string, error) {
		workingDirectoryCalls++
		return root, nil
	}
	stdout, stderr, exit := runTestApp(application, "--project", "known", "project", "show")
	if exit != 3 || stdout != "" {
		t.Fatalf("valid project show = exit %d stdout %q stderr %q, want discovery failure", exit, stdout, stderr)
	}
	assertCompactErrorCode(t, stderr, "database_not_found")
	if workingDirectoryCalls != 1 {
		t.Fatalf("valid project show crossed working-directory boundary %d times, want 1", workingDirectoryCalls)
	}
}

func TestGitLinkedWorktreesShareOneLogicalProject(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "worktree database 界")
	mainWorkTree := filepath.Join(common, "main work tree")
	linkedWorkTree := filepath.Join(common, "linked 工作 tree")
	secondLinkedWorkTree := filepath.Join(common, "second linked tree")
	if err := os.MkdirAll(mainWorkTree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(mainWorkTree, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "add", "README")
	runGitTest(
		t,
		mainWorkTree,
		"-c", "user.name=Pellets Test",
		"-c", "user.email=pellets@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", "fixture",
	)
	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "linked-project-test", linkedWorkTree)
	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "second-linked-project-test", secondLinkedWorkTree)
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := mainWorkTree
	application := projectTestApp(&current)
	mainProject := runCurrentProject(t, application)
	current = linkedWorkTree
	linkedProject := runCurrentProject(t, application)
	current = secondLinkedWorkTree
	allWorkspaces := runCurrentProject(t, application)
	if mainProject.Code != linkedProject.Code || linkedProject.Code != allWorkspaces.Code || len(allWorkspaces.Workspaces) != 3 {
		t.Fatalf("linked worktree project snapshots = %#v %#v %#v", mainProject, linkedProject, allWorkspaces)
	}
	gotRoots := []string{allWorkspaces.Workspaces[0].RootPath, allWorkspaces.Workspaces[1].RootPath, allWorkspaces.Workspaces[2].RootPath}
	wantRoots := []string{"main work tree", "linked 工作 tree", "second linked tree"}
	if !reflect.DeepEqual(gotRoots, wantRoots) {
		t.Fatalf("workspace roots = %q, want %q", gotRoots, wantRoots)
	}
	if shown := decodeCurrentProject(t, application); !reflect.DeepEqual(shown, allWorkspaces) {
		t.Fatalf("linked current project = %#v, want %#v", shown, allWorkspaces)
	}
	if projects := runProjectList(t, application); len(projects) != 1 || !reflect.DeepEqual(projects[0], allWorkspaces) {
		t.Fatalf("projects = %#v, want one shared project", projects)
	}
	formerCode := allWorkspaces.Code
	stdout, stderr, exit := runTestApp(application, "project", "rename", "renamed")
	if exit != 0 || stderr != "" {
		t.Fatalf("linked-worktree rename = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	for _, root := range []string{mainWorkTree, linkedWorkTree, secondLinkedWorkTree} {
		current = root
		project := runCurrentProject(t, application)
		if project.Code != "renamed" || len(project.Workspaces) != 3 || len(project.Redirects) != 1 || project.Redirects[0].Code != formerCode {
			t.Fatalf("project after rename from %s = %#v", root, project)
		}
	}
	stdout, stderr, exit = runTestApp(application, "project", "show", formerCode)
	if exit != 0 || stderr != "" || decodeProjectResult(t, stdout, "project show").Code != "renamed" {
		t.Fatalf("former linked-worktree code = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}

func TestWorkspaceMoveRemovalAndDuplicateRegistrationAreSafe(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "worktree lifecycle root")
	mainRoot := filepath.Join(common, "main")
	linkedRoot := filepath.Join(common, "linked original")
	movedRoot := filepath.Join(common, "linked moved 界")
	duplicateRoot := filepath.Join(common, "linked duplicate")
	replacementRoot := filepath.Join(common, "replacement")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainRoot, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(mainRoot, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainRoot, "add", "README")
	runGitTest(t, mainRoot, "-c", "user.name=Pellets Test", "-c", "user.email=pellets@example.invalid", "commit", "--quiet", "-m", "fixture")
	runGitTest(t, mainRoot, "worktree", "add", "--quiet", "-b", "workspace-lifecycle", linkedRoot)
	if _, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 {
		t.Fatalf("init-db = exit %d stderr %q", exit, stderr)
	}

	current := mainRoot
	application := projectTestApp(&current)
	runCurrentProject(t, application)
	current = linkedRoot
	registered := runCurrentProject(t, application)
	if len(registered.Workspaces) != 2 {
		t.Fatalf("registered workspaces = %#v", registered.Workspaces)
	}

	if err := os.CopyFS(duplicateRoot, os.DirFS(linkedRoot)); err != nil {
		t.Fatal(err)
	}
	current = duplicateRoot
	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"workspace_identity_conflict"`) {
		t.Fatalf("duplicate bootstrap = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	runGitTest(t, mainRoot, "worktree", "move", linkedRoot, movedRoot)
	current = movedRoot
	moved := runCurrentProject(t, application)
	if len(moved.Workspaces) != 2 || moved.Workspaces[1].RootPath != "linked moved 界" {
		t.Fatalf("moved workspaces = %#v", moved.Workspaces)
	}

	runGitTest(t, mainRoot, "worktree", "remove", movedRoot)
	runGitTest(t, mainRoot, "worktree", "add", "--quiet", "-b", "workspace-replacement", replacementRoot)
	current = replacementRoot
	withReplacement := runCurrentProject(t, application)
	if len(withReplacement.Workspaces) != 3 || withReplacement.Workspaces[1].RootPath != "linked moved 界" || withReplacement.Workspaces[2].RootPath != "replacement" {
		t.Fatalf("removed/stale registration behavior = %#v", withReplacement.Workspaces)
	}
}

func projectTestApp(current *string, afterPelletOpen ...func(string) error) *App {
	initializer := app.DatabaseInitializer{
		Path: discovery.DatabasePath,
		Open: func(ctx context.Context, path string) (app.DatabaseHandle, error) {
			return sqlite.Open(ctx, path)
		},
		GitSafety: discovery.GitSafety{},
	}
	manager := app.ProjectManager{
		Discover: app.ProjectDiscovery{
			FindGitIdentity: discovery.FindGitIdentity,
			FindDatabase: func(workingDirectory string) (app.Database, error) {
				database, err := discovery.FindDatabase(workingDirectory)
				return app.Database{Root: database.Root, Path: database.Path}, err
			},
			NormalizePath: discovery.NormalizeLocalPath,
			ResolvePath:   discovery.ResolveLocalPath,
			PathExists:    discovery.PathExists,
		},
		Initialize: initializer.Init,
		Open: func(ctx context.Context, path string) (storage.ProjectDatabase, error) {
			return sqlite.OpenProjectDatabase(ctx, path)
		},
		GitSafety: discovery.GitSafety{},
	}
	pelletManager := app.PelletManager{
		Projects: manager,
		Open: func(ctx context.Context, path string) (storage.PelletRepository, error) {
			repository, err := sqlite.OpenPelletRepository(ctx, path)
			if err != nil {
				return nil, err
			}
			if len(afterPelletOpen) > 0 {
				if err := afterPelletOpen[0](path); err != nil {
					_ = repository.Close()
					return nil, err
				}
			}
			return repository, nil
		},
	}
	memoryManager := app.MemoryManager{
		Projects: manager,
		Open: func(ctx context.Context, path string) (storage.MemoryRepository, error) {
			return sqlite.OpenMemoryRepository(ctx, path)
		},
	}
	application := New(
		"test",
		InitDBCommand(initializer), ProjectCommand(manager),
		AddCommand(pelletManager), MoveCommand(pelletManager), ListCommand(pelletManager), ShowCommand(pelletManager),
		SearchCommand(pelletManager),
		PurgeCommand(pelletManager),
		EditCommand(pelletManager), NextCommand(pelletManager), StartCommand(pelletManager),
		StartNextCommand(pelletManager), ReleaseCommand(pelletManager), CloseCommand(pelletManager),
		ReopenCommand(pelletManager), DeferCommand(pelletManager),
		MemoryCommand(memoryManager),
	).WithCurrentWorkspaceBootstrap(func(ctx context.Context, workingDirectory string) (discovery.Database, error) {
		database, err := manager.BootstrapCurrent(ctx, workingDirectory)
		return discovery.Database{Root: database.Root, Path: database.Path}, err
	})
	application.workingDirectory = func() (string, error) { return *current, nil }
	return application
}

func runCurrentProject(t *testing.T, application *App) projectData {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	return decodeProjectResult(t, stdout, "project show")
}

func runProjectList(t *testing.T, application *App) projectListData {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, "project", "list")
	if exit != 0 || stderr != "" {
		t.Fatalf("project list = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	var result struct {
		SchemaVersion int             `json:"schema_version"`
		Command       string          `json:"command"`
		Data          projectListData `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode project list %q: %v", stdout, err)
	}
	if result.SchemaVersion != 1 || result.Command != "project list" || result.Data == nil {
		t.Fatalf("project list envelope = %#v", result)
	}
	return result.Data
}

func decodeProjectResult(t *testing.T, output, command string) projectData {
	t.Helper()
	var result struct {
		SchemaVersion int         `json:"schema_version"`
		Command       string      `json:"command"`
		Data          projectData `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode %s output %q: %v", command, output, err)
	}
	if result.SchemaVersion != 1 || result.Command != command || result.Data.Code == "" || result.Data.GitCommonDir == "" || len(result.Data.Workspaces) == 0 || result.Data.CreatedAt == "" || result.Data.UpdatedAt == "" {
		t.Fatalf("%s envelope = %#v", command, result)
	}
	return result.Data
}

func decodeProjectRenameResult(t *testing.T, raw string) projectRenameData {
	t.Helper()
	var result struct {
		SchemaVersion int               `json:"schema_version"`
		Command       string            `json:"command"`
		Data          projectRenameData `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode project rename output %q: %v", raw, err)
	}
	if result.SchemaVersion != 1 || result.Command != "project rename" || result.Data.Project.Code == "" {
		t.Fatalf("project rename envelope = %#v", result)
	}
	return result.Data
}

func decodeCurrentProject(t *testing.T, application *App) projectData {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 0 || stderr != "" {
		t.Fatalf("project show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	return decodeProjectResult(t, stdout, "project show")
}

func assertCompactErrorCode(t *testing.T, output, wantCode string) {
	t.Helper()
	if strings.Count(output, "\n") != 1 || !strings.HasSuffix(output, "\n") {
		t.Fatalf("error output is not one compact line: %q", output)
	}
	var envelope struct {
		SchemaVersion int `json:"schema_version"`
		Error         struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode error output %q: %v", output, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Error.Code != wantCode {
		t.Fatalf("error envelope = %#v, want code %q", envelope, wantCode)
	}
}
