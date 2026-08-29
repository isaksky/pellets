package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pellets/internal/app"
	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

func TestInitDBCreatesExactPathAndNeverOverwrites(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "database root with spaces")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	application := initDBTestApp(root)

	stdout, stderr, exit := runTestApp(application, "init-db")
	if exit != 0 || stderr != "" {
		t.Fatalf("init-db exit = %d, stderr = %q", exit, stderr)
	}
	databasePath := discovery.DatabasePath(root)
	var result struct {
		SchemaVersion int    `json:"schema_version"`
		Command       string `json:"command"`
		Data          struct {
			DatabasePath string `json:"database_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if result.SchemaVersion != 1 || result.Command != "init-db" || result.Data.DatabasePath != databasePath {
		t.Fatalf("init-db result = %#v, want database path %q", result, databasePath)
	}
	entries, err := os.ReadDir(filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != discovery.DatabaseFilename || entries[0].IsDir() {
		t.Fatalf(".pellets entries = %v, want only %s", entryNames(entries), discovery.DatabaseFilename)
	}
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit = runTestApp(application, "init-db")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"database_already_exists"`) {
		t.Fatalf("second init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("existing database changed during rejected initialization")
	}
}

func TestCommandDatabaseDiscoveryNoAncestorNestedAndSpaces(t *testing.T) {
	t.Parallel()

	outer := filepath.Join(t.TempDir(), "outer database with spaces")
	inner := filepath.Join(outer, "inner database with spaces")
	deep := filepath.Join(inner, "deep path")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	current := deep
	application := New("test", discoveredDatabaseCommand())
	application.workingDirectory = func() (string, error) { return current, nil }

	stdout, stderr, exit := runTestApp(application, "database-location")
	if exit != 3 || stdout != "" || !strings.Contains(stderr, `"code":"database_not_found"`) {
		t.Fatalf("no-ancestor discovery = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	initOuter := initDBTestApp(outer)
	if stdout, stderr, exit = runTestApp(initOuter, "init-db"); exit != 0 {
		t.Fatalf("outer init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	assertDiscoveredDatabase(t, application, discovery.DatabasePath(outer))

	initInner := initDBTestApp(inner)
	if stdout, stderr, exit = runTestApp(initInner, "init-db"); exit != 0 {
		t.Fatalf("inner init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	assertDiscoveredDatabase(t, application, discovery.DatabasePath(inner))
}

func TestInitDBUsesOnlyLocalGitExclude(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "Git work tree with spaces")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	gitignorePath := filepath.Join(repository, ".gitignore")
	gitignoreContents := []byte("keep-me\n")
	if err := os.WriteFile(gitignorePath, gitignoreContents, 0o644); err != nil {
		t.Fatal(err)
	}
	databaseRoot := filepath.Join(repository, "nested root")
	if err := os.Mkdir(databaseRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runTestApp(initDBTestApp(databaseRoot), "init-db")
	if exit != 0 || stderr != "" {
		t.Fatalf("init-db in Git = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	gotGitignore, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotGitignore, gitignoreContents) {
		t.Fatalf(".gitignore changed from %q to %q", gitignoreContents, gotGitignore)
	}

	excludePath := gitOutput(t, repository, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(repository, excludePath)
	}
	excludeContents, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if countExactLines(excludeContents, ".pellets/") != 1 {
		t.Fatalf("local exclude %q does not contain exactly one .pellets/ line:\n%s", excludePath, excludeContents)
	}
	relativeDatabase, err := filepath.Rel(repository, discovery.DatabasePath(databaseRoot))
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "check-ignore", "--quiet", "--", filepath.ToSlash(relativeDatabase))
	if commits := gitOutput(t, repository, "rev-list", "--all", "--count"); commits != "0" {
		t.Fatalf("commit count = %q, want 0", commits)
	}
}

func TestInitDBRejectsDatabaseTrackedOnlyInGitIndex(t *testing.T) {
	t.Parallel()

	repository := filepath.Join(t.TempDir(), "tracked database repository")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")
	databasePath := discovery.DatabasePath(repository)
	if err := os.Mkdir(filepath.Dir(databasePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, []byte("tracked sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "-f", "--", filepath.ToSlash(filepath.Join(discovery.MetadataDirectory, discovery.DatabaseFilename)))
	if err := os.Remove(databasePath); err != nil {
		t.Fatal(err)
	}
	excludePath := gitOutput(t, repository, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(repository, excludePath)
	}
	excludeBefore, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exit := runTestApp(initDBTestApp(repository), "init-db")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"database_already_tracked"`) {
		t.Fatalf("tracked init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if _, err := os.Stat(databasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database path after rejection: %v", err)
	}
	excludeAfter, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(excludeAfter, excludeBefore) {
		t.Fatal("local exclude changed while rejecting a tracked database")
	}
}

func TestInitDBUsesGitResolvedExcludeForLinkedWorktree(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	mainWorkTree := filepath.Join(temporary, "main work tree")
	linkedWorkTree := filepath.Join(temporary, "linked work tree")
	if err := os.Mkdir(mainWorkTree, 0o755); err != nil {
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
	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "linked-test", linkedWorkTree)
	commitsBefore := gitOutput(t, linkedWorkTree, "rev-list", "--all", "--count")

	stdout, stderr, exit := runTestApp(initDBTestApp(linkedWorkTree), "init-db")
	if exit != 0 || stderr != "" {
		t.Fatalf("init-db in linked worktree = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	excludePath := gitOutput(t, linkedWorkTree, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(linkedWorkTree, excludePath)
	}
	excludeContents, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if countExactLines(excludeContents, ".pellets/") != 1 {
		t.Fatalf("linked-worktree exclude %q does not contain .pellets/:\n%s", excludePath, excludeContents)
	}
	runGitTest(t, linkedWorkTree, "check-ignore", "--quiet", "--", ".pellets/pellets.db")
	if commitsAfter := gitOutput(t, linkedWorkTree, "rev-list", "--all", "--count"); commitsAfter != commitsBefore {
		t.Fatalf("commit count changed from %q to %q", commitsBefore, commitsAfter)
	}
}

func TestInitDBRejectsProjectOverride(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stdout, stderr, exit := runTestApp(initDBTestApp(root), "--project", "foo", "init-db")
	if exit != 2 || stdout != "" || !strings.Contains(stderr, `"code":"project_not_allowed"`) {
		t.Fatalf("init-db --project = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, discovery.MetadataDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".pellets after rejected --project: %v", err)
	}
}

func initDBTestApp(workingDirectory string) *App {
	initializer := app.DatabaseInitializer{
		Path: discovery.DatabasePath,
		Open: func(ctx context.Context, path string) (app.DatabaseHandle, error) {
			return sqlite.Open(ctx, path)
		},
		GitSafety: discovery.GitSafety{},
	}
	application := New("test", InitDBCommand(initializer))
	application.workingDirectory = func() (string, error) { return workingDirectory, nil }
	return application
}

func discoveredDatabaseCommand() Command {
	return Command{
		Name: "database-location",
		Run: func(_ context.Context, invocation Invocation) (any, error) {
			return struct {
				Path string `json:"path"`
			}{Path: invocation.Database.Path}, nil
		},
	}
}

func assertDiscoveredDatabase(t *testing.T, application *App, wantPath string) {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, "database-location")
	if exit != 0 || stderr != "" {
		t.Fatalf("database discovery = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if !strings.Contains(stdout, `"path":`+strconvQuote(wantPath)) {
		t.Fatalf("database discovery stdout = %q, want path %q", stdout, wantPath)
	}
}

func runTestApp(application *App, args ...string) (stdout, stderr string, exit int) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	exit = application.Run(args, &stdoutBuffer, &stderrBuffer)
	return stdoutBuffer.String(), stderrBuffer.String(), exit
}

func runGitTest(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func countExactLines(contents []byte, want string) int {
	count := 0
	for _, line := range bytes.Split(contents, []byte{'\n'}) {
		if string(bytes.TrimSuffix(line, []byte{'\r'})) == want {
			count++
		}
	}
	return count
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
