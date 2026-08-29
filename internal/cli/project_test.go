package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestInitCreatesDatabaseAtGitRootAndProjectCommandsResolveCurrent(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "Git project with spaces and 世界")
	nested := filepath.Join(repository, "nested", "working directory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")

	current := nested
	application := projectTestApp(&current)
	stdout, stderr, exit := runTestApp(application, "init", "--code", "demo-1")
	if exit != 0 || stderr != "" {
		t.Fatalf("init = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	initialized := decodeProjectResult(t, stdout, "init")
	if initialized.Code != "demo-1" || len(initialized.Workspaces) != 1 || initialized.Workspaces[0].RootPath != "." || initialized.GitCommonDir != ".git" {
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

	stdout, stderr, exit = runTestApp(application, "project", "show", "demo-1")
	if exit != 0 || stderr != "" {
		t.Fatalf("project show demo-1 = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if named := decodeProjectResult(t, stdout, "project show"); !reflect.DeepEqual(named, initialized) {
		t.Fatalf("named project = %#v, want %#v", named, initialized)
	}

	stdout, stderr, exit = runTestApp(application, "init", "--code=demo-1")
	if exit != 0 || stderr != "" {
		t.Fatalf("idempotent init = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if repeated := decodeProjectResult(t, stdout, "init"); !reflect.DeepEqual(repeated, initialized) {
		t.Fatalf("idempotent project = %#v, want unchanged %#v", repeated, initialized)
	}

	projects := runProjectList(t, application)
	if !reflect.DeepEqual(projects, projectListData{initialized}) {
		t.Fatalf("project list = %#v, want one unchanged row", projects)
	}

	stdout, stderr, exit = runTestApp(application, "init", "--code", "changed")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"project_repository_already_registered"`) {
		t.Fatalf("changed immutable code = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if projects = runProjectList(t, application); !reflect.DeepEqual(projects, projectListData{initialized}) {
		t.Fatalf("projects changed after conflict: %#v", projects)
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
	firstProject := runProjectInit(t, application, "svc-a")
	if firstProject.Workspaces[0].RootPath != "service alpha" {
		t.Fatalf("first root path = %q", firstProject.Workspaces[0].RootPath)
	}
	current = second
	secondProject := runProjectInit(t, application, "svc-b")
	if secondProject.Workspaces[0].RootPath != "service βeta" {
		t.Fatalf("second root path = %q", secondProject.Workspaces[0].RootPath)
	}

	current = third
	stdout, stderr, exit := runTestApp(application, "init", "--code", "svc-a")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"project_code_already_registered"`) {
		t.Fatalf("duplicate code = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current = common
	projects := runProjectList(t, application)
	want := projectListData{firstProject, secondProject}
	if !reflect.DeepEqual(projects, want) {
		t.Fatalf("project list = %#v, want %#v", projects, want)
	}
	stdout, stderr, exit = runTestApp(application, "project", "show", "svc-b")
	if exit != 0 || stderr != "" {
		t.Fatalf("named show outside Git = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if shown := decodeProjectResult(t, stdout, "project show"); !reflect.DeepEqual(shown, secondProject) {
		t.Fatalf("shown project = %#v, want %#v", shown, secondProject)
	}
	stdout, stderr, exit = runTestApp(application, "--project", "svc-a", "project", "show")
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

func TestInitRejectsInvalidCodesBeforeCreatingDatabase(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "code validation repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	current := repository
	application := projectTestApp(&current)

	invalid := []string{"", "-foo", "foo-", "ABCDEFGHIJKLM", "Upper", "foo_bar", "pellet界"}
	for _, code := range invalid {
		arguments := []string{"init", "--code", code}
		if strings.HasPrefix(code, "-") {
			arguments = []string{"init", "--code=" + code}
		}
		stdout, stderr, exit := runTestApp(application, arguments...)
		if exit != 2 || stdout != "" || !strings.Contains(stderr, `"code":"invalid_project_code"`) && code != "" {
			t.Errorf("init code %q = exit %d stdout %q stderr %q", code, exit, stdout, stderr)
		}
		if code == "" && !strings.Contains(stderr, `"code":"missing_flag_value"`) {
			t.Errorf("empty code error = %q", stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(repository, discovery.MetadataDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid code created metadata: %v", err)
	}
}

func TestInitStoresAbsoluteIdentityWhenRepositoryIsOutsideDatabaseRoot(t *testing.T) {
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
	stdout, stderr, exit := runTestApp(application, "init", "--code", "outer")
	if exit != 0 || stderr != "" {
		t.Fatalf("outside init = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	project := decodeProjectResult(t, stdout, "init")
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if project.GitCommonDirRelative || project.Workspaces[0].RootPathRelative || !strings.EqualFold(project.Workspaces[0].RootPath, filepath.ToSlash(canonicalRepository)) {
		t.Fatalf("outside project paths = %#v", project)
	}
}

func TestUnregisteredSiblingAndProjectCommandValidationFailCleanly(t *testing.T) {
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
	runProjectInit(t, application, "known")
	current = unregistered

	stdout, stderr, exit := runTestApp(application, "project", "show")
	if exit != 3 || stdout != "" || !strings.Contains(stderr, `"code":"project_not_registered"`) {
		t.Fatalf("unregistered show = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	tests := []struct {
		args []string
		code string
	}{
		{[]string{"project"}, "missing_subcommand"},
		{[]string{"project", "wat"}, "unknown_subcommand"},
		{[]string{"project", "list", "extra"}, "unexpected_argument"},
		{[]string{"project", "show", "known", "extra"}, "unexpected_argument"},
		{[]string{"--project", "known", "project", "list"}, "project_not_allowed"},
		{[]string{"--project", "known", "project", "show", "known"}, "conflicting_project_selection"},
		{[]string{"--project", "known", "init", "--code", "other"}, "project_not_allowed"},
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
		InitCommand(manager),
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
		{name: "init project override", args: []string{"--project", "known", "init", "--code", "other"}, code: "project_not_allowed"},
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
	mainProject := runProjectInit(t, application, "shared")
	current = linkedWorkTree
	linkedProject := runProjectInit(t, application, "shared")
	current = secondLinkedWorkTree
	allWorkspaces := runProjectInit(t, application, "shared")
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
	runProjectInit(t, application, "shared")
	current = linkedRoot
	registered := runProjectInit(t, application, "shared")
	if len(registered.Workspaces) != 2 {
		t.Fatalf("registered workspaces = %#v", registered.Workspaces)
	}

	if err := os.CopyFS(duplicateRoot, os.DirFS(linkedRoot)); err != nil {
		t.Fatal(err)
	}
	current = duplicateRoot
	stdout, stderr, exit := runTestApp(application, "init", "--code", "shared")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"workspace_identity_conflict"`) {
		t.Fatalf("duplicate init = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	runGitTest(t, mainRoot, "worktree", "move", linkedRoot, movedRoot)
	current = movedRoot
	moved := runProjectInit(t, application, "shared")
	if len(moved.Workspaces) != 2 || moved.Workspaces[1].RootPath != "linked moved 界" {
		t.Fatalf("moved workspaces = %#v", moved.Workspaces)
	}

	runGitTest(t, mainRoot, "worktree", "remove", movedRoot)
	runGitTest(t, mainRoot, "worktree", "add", "--quiet", "-b", "workspace-replacement", replacementRoot)
	current = replacementRoot
	withReplacement := runProjectInit(t, application, "shared")
	if len(withReplacement.Workspaces) != 3 || withReplacement.Workspaces[1].RootPath != "linked moved 界" || withReplacement.Workspaces[2].RootPath != "replacement" {
		t.Fatalf("removed/stale registration behavior = %#v", withReplacement.Workspaces)
	}
}

func projectTestApp(current *string) *App {
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
	application := New("test", InitDBCommand(initializer), InitCommand(manager), ProjectCommand(manager))
	application.workingDirectory = func() (string, error) { return *current, nil }
	return application
}

func runProjectInit(t *testing.T, application *App, code string) projectData {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, "init", "--code", code)
	if exit != 0 || stderr != "" {
		t.Fatalf("init %s = exit %d stdout %q stderr %q", code, exit, stdout, stderr)
	}
	return decodeProjectResult(t, stdout, "init")
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
