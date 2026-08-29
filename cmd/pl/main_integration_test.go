package main

import (
	"bytes"
	"context"
	"database/sql"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

func TestFoundationCompiledExecutable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("foundation integration tests require native Git: %v", err)
	}
	executable := buildFoundationExecutable(t)

	t.Run("process contract", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "process contract with spaces and 界")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}

		result := runFoundationCLIWithBlockedStdin(t, executable, root, "--version")
		assertFoundationResult(t, result, 0, "pl dev (JSON schema 1)\n", "")

		result = runFoundationCLI(t, executable, root, "unknown-foundation-command")
		assertFoundationResult(t, result, 2, "", foundationErrorJSON(
			"unknown_command",
			"unknown command \"unknown-foundation-command\"",
			map[string]any{"command": "unknown-foundation-command"},
		))

		result = runFoundationCLI(t, executable, root, "project", "list")
		assertFoundationResult(t, result, 3, "", foundationErrorJSON(
			"database_not_found",
			"no Pellets database was found in the current directory or its ancestors",
			map[string]any{"start_path": foundationCanonicalPath(t, root)},
		))

		metadataPath := filepath.Join(root, discovery.MetadataDirectory)
		if err := os.Mkdir(metadataPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(discovery.DatabasePath(root), []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		result = runFoundationCLI(t, executable, root, "project", "list")
		assertFoundationResult(t, result, 5, "", foundationErrorJSON(
			"database_open_failed",
			"could not open database",
			nil,
		))
	})

	t.Run("Git root initialization and immutable registration", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "foundation repository with spaces and 世界")
		createFoundationRepository(t, repository)
		nested := filepath.Join(repository, "nested working", "目录")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}

		headBefore := foundationGitText(t, repository, "rev-parse", "HEAD")
		indexBefore := foundationGitBytes(t, repository, "ls-files", "--stage", "-z")
		statusBefore := foundationGitBytes(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
		excludePath := foundationExcludePath(t, repository)
		excludeBefore := readFoundationFile(t, excludePath)

		result := runFoundationCLI(t, executable, nested, "init", "--code", "foundation")
		project := decodeFoundationSuccess[foundationProject](t, result, "init")
		if project.Code != "foundation" || project.GitCommonDir != ".git" || len(project.Workspaces) != 1 || project.Workspaces[0].RootPath != "." || project.CreatedAt != project.UpdatedAt {
			t.Fatalf("initialized project = %#v", project)
		}
		assertFoundationTimestamp(t, project.CreatedAt)

		canonicalRepository := foundationCanonicalPath(t, repository)
		databasePath := discovery.DatabasePath(canonicalRepository)
		if _, err := os.Stat(databasePath); err != nil {
			t.Fatalf("database was not placed at Git root %q: %v", databasePath, err)
		}
		if _, err := os.Stat(discovery.DatabasePath(nested)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database unexpectedly exists below Git root: %v", err)
		}
		assertFoundationDatabase(t, databasePath, 1)

		shown := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, nested, "project", "show"),
			"project show",
		)
		if !reflect.DeepEqual(shown, project) {
			t.Fatalf("current project = %#v, want %#v", shown, project)
		}

		repeatedResult := runFoundationCLI(t, executable, nested, "init", "--code=foundation")
		repeated := decodeFoundationSuccess[foundationProject](t, repeatedResult, "init")
		if !reflect.DeepEqual(repeated, project) || repeatedResult.stdout != result.stdout {
			t.Fatalf("idempotent registration = %#v, want unchanged %#v", repeated, project)
		}

		conflict := runFoundationCLI(t, executable, nested, "init", "--code", "changed")
		assertFoundationResult(t, conflict, 4, "", foundationErrorJSON(
			"project_repository_already_registered",
			"the Git repository is already registered with a different immutable code",
			map[string]any{
				"existing_code":  "foundation",
				"requested_code": "changed",
			},
		))

		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, repository, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(projects, []foundationProject{project}) {
			t.Fatalf("projects = %#v, want only %#v", projects, project)
		}

		assertFoundationGitState(t, repository, headBefore, indexBefore, statusBefore)
		assertFoundationPathAbsent(t, filepath.Join(repository, ".gitignore"))
		excludeAfter := readFoundationFile(t, excludePath)
		if want := appendFoundationExclude(excludeBefore); !bytes.Equal(excludeAfter, want) {
			t.Fatalf("local exclude = %q, want %q", excludeAfter, want)
		}
		for _, path := range discovery.DatabasePaths(databasePath) {
			relative, err := filepath.Rel(canonicalRepository, path)
			if err != nil {
				t.Fatal(err)
			}
			runFoundationGit(t, repository, "check-ignore", "--quiet", "--no-index", "--", filepath.ToSlash(relative))
		}
	})

	t.Run("tracked database rejection is read only", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "tracked database repository 界")
		createFoundationRepository(t, repository)
		databasePath := discovery.DatabasePath(foundationCanonicalPath(t, repository))
		if err := os.Mkdir(filepath.Dir(databasePath), 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := []byte("tracked database sentinel")
		if err := os.WriteFile(databasePath, sentinel, 0o600); err != nil {
			t.Fatal(err)
		}
		runFoundationGit(t, repository, "add", "-f", "--", filepath.ToSlash(filepath.Join(discovery.MetadataDirectory, discovery.DatabaseFilename)))

		headBefore := foundationGitText(t, repository, "rev-parse", "HEAD")
		indexBefore := foundationGitBytes(t, repository, "ls-files", "--stage", "-z")
		statusBefore := foundationGitBytes(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
		excludePath := foundationExcludePath(t, repository)
		excludeBefore := readFoundationFile(t, excludePath)

		result := runFoundationCLI(t, executable, repository, "init-db")
		assertFoundationResult(t, result, 4, "", foundationErrorJSON(
			"database_already_tracked",
			"the Pellets database path is already tracked by Git",
			map[string]any{"database_path": databasePath},
		))
		if got := readFoundationFile(t, databasePath); !bytes.Equal(got, sentinel) {
			t.Fatalf("tracked database changed from %q to %q", sentinel, got)
		}
		assertFoundationGitState(t, repository, headBefore, indexBefore, statusBefore)
		if excludeAfter := readFoundationFile(t, excludePath); !bytes.Equal(excludeAfter, excludeBefore) {
			t.Fatalf("local exclude changed from %q to %q", excludeBefore, excludeAfter)
		}
		assertFoundationPathAbsent(t, filepath.Join(repository, ".gitignore"))
	})

	t.Run("nearest nested database wins", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "nearest database root 界")
		repository := filepath.Join(common, "outer repository with spaces")
		createFoundationRepository(t, repository)

		outerDatabase := discovery.DatabasePath(foundationCanonicalPath(t, common))
		decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		outerProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, repository, "init", "--code", "outer"),
			"init",
		)

		innerRoot := filepath.Join(repository, "nested database root 深")
		deep := filepath.Join(innerRoot, "deeper working directory")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		innerDatabase := discovery.DatabasePath(foundationCanonicalPath(t, innerRoot))
		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, innerRoot, "init-db"),
			"init-db",
		)
		if initialized.DatabasePath != innerDatabase {
			t.Fatalf("nested database path = %q, want %q", initialized.DatabasePath, innerDatabase)
		}

		innerProjects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, deep, "project", "list"),
			"project list",
		)
		if innerProjects == nil || len(innerProjects) != 0 {
			t.Fatalf("nearest nested project list = %#v, want []", innerProjects)
		}
		outerProjects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, repository, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(outerProjects, []foundationProject{outerProject}) {
			t.Fatalf("outer project list = %#v, want %#v", outerProjects, []foundationProject{outerProject})
		}
		assertFoundationDatabase(t, outerDatabase, 1)
		assertFoundationDatabase(t, innerDatabase, 0)
	})

	t.Run("sibling repositories share one database", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "sibling database root 共通")
		firstRoot := filepath.Join(common, "service alpha")
		secondRoot := filepath.Join(common, "service βeta")
		duplicateRoot := filepath.Join(common, "service duplicate")
		for _, root := range []string{firstRoot, secondRoot, duplicateRoot} {
			createFoundationRepository(t, root)
		}

		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		if initialized.DatabasePath != discovery.DatabasePath(foundationCanonicalPath(t, common)) {
			t.Fatalf("common database path = %q", initialized.DatabasePath)
		}
		first := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, firstRoot, "init", "--code", "svc-a"),
			"init",
		)
		second := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, secondRoot, "init", "--code", "svc-b"),
			"init",
		)
		if first.Workspaces[0].RootPath != "service alpha" || second.Workspaces[0].RootPath != "service βeta" {
			t.Fatalf("sibling paths = %q and %q", first.Workspaces[0].RootPath, second.Workspaces[0].RootPath)
		}

		conflict := runFoundationCLI(t, executable, duplicateRoot, "init", "--code", "svc-a")
		assertFoundationResult(t, conflict, 4, "", foundationErrorJSON(
			"project_code_already_registered",
			"the project code is already registered for a different Git repository",
			map[string]any{
				"code": "svc-a",
			},
		))

		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, common, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(projects, []foundationProject{first, second}) {
			t.Fatalf("sibling projects = %#v, want %#v", projects, []foundationProject{first, second})
		}
		assertFoundationDatabase(t, initialized.DatabasePath, 2)
	})

	t.Run("linked worktrees share one logical project", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "worktree database root 界")
		mainRoot := filepath.Join(common, "main work tree")
		linkedRoot := filepath.Join(common, "linked 工作 tree")
		secondLinkedRoot := filepath.Join(common, "second linked tree")
		createFoundationRepository(t, mainRoot)
		if _, err := foundationGitCommand(mainRoot, "worktree", "list", "--porcelain"); err != nil {
			t.Skipf("Git worktrees are unavailable: %v", err)
		}
		if output, err := foundationGitCommand(
			mainRoot,
			"worktree", "add", "--quiet", "-b", "pellets-foundation-linked", linkedRoot,
		); err != nil {
			t.Fatalf("add linked worktree: %v\n%s", err, output)
		}
		if output, err := foundationGitCommand(
			mainRoot,
			"worktree", "add", "--quiet", "-b", "pellets-foundation-linked-two", secondLinkedRoot,
		); err != nil {
			t.Fatalf("add second linked worktree: %v\n%s", err, output)
		}

		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		mainProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, mainRoot, "init", "--code", "worktree"),
			"init",
		)
		linkedProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, linkedRoot, "init", "--code", "worktree"),
			"init",
		)
		allWorkspaces := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, secondLinkedRoot, "init", "--code", "worktree"),
			"init",
		)
		if mainProject.Code != linkedProject.Code || linkedProject.Code != allWorkspaces.Code || len(allWorkspaces.Workspaces) != 3 {
			t.Fatalf("worktree projects = %#v %#v %#v", mainProject, linkedProject, allWorkspaces)
		}

		shown := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, secondLinkedRoot, "project", "show"),
			"project show",
		)
		if !reflect.DeepEqual(shown, allWorkspaces) {
			t.Fatalf("linked current project = %#v, want %#v", shown, allWorkspaces)
		}
		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, common, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(projects, []foundationProject{allWorkspaces}) {
			t.Fatalf("worktree projects = %#v", projects)
		}
		assertFoundationDatabase(t, initialized.DatabasePath, 1)
		database, err := sqlite.Open(context.Background(), initialized.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM project_workspaces", 3)
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

type foundationResult struct {
	stdout string
	stderr string
	exit   int
}

type foundationSuccess[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Data          T      `json:"data"`
}

type foundationInitDB struct {
	DatabasePath string `json:"database_path"`
}

type foundationProject struct {
	Code                 string                `json:"code"`
	GitCommonDir         string                `json:"git_common_dir"`
	GitCommonDirRelative bool                  `json:"git_common_dir_relative"`
	Workspaces           []foundationWorkspace `json:"workspaces"`
	CreatedAt            string                `json:"created_at"`
	UpdatedAt            string                `json:"updated_at"`
}

type foundationWorkspace struct {
	ID               int64  `json:"id"`
	RootPath         string `json:"root_path"`
	RootPathRelative bool   `json:"root_path_relative"`
	GitDir           string `json:"git_dir"`
	GitDirRelative   bool   `json:"git_dir_relative"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type foundationError struct {
	SchemaVersion int `json:"schema_version"`
	Error         struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

func buildFoundationExecutable(t *testing.T) string {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	name := "pl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-trimpath", "-o", executable, "./cmd/pl")
	command.Dir = repositoryRoot
	command.Env = append(
		os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+runtime.GOOS,
		"GOARCH="+runtime.GOARCH,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build foundation executable: %v\n%s", err, output)
	}

	info, err := buildinfo.ReadFile(executable)
	if err != nil {
		t.Fatalf("read foundation executable metadata: %v", err)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	for key, want := range map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        runtime.GOOS,
		"GOARCH":      runtime.GOARCH,
	} {
		if got := settings[key]; got != want {
			t.Fatalf("compiled executable %s = %q, want %q", key, got, want)
		}
	}
	return executable
}

func runFoundationCLI(t *testing.T, executable, directory string, args ...string) foundationResult {
	t.Helper()
	command, stdout, stderr := foundationCLICommand(executable, directory, args...)
	err := command.Run()
	return foundationProcessResult(t, stdout, stderr, err)
}

func runFoundationCLIWithBlockedStdin(t *testing.T, executable, directory string, args ...string) foundationResult {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	command, stdout, stderr := foundationCLICommand(executable, directory, args...)
	command.Stdin = reader
	if err := command.Start(); err != nil {
		t.Fatalf("start compiled CLI: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return foundationProcessResult(t, stdout, stderr, err)
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatal("compiled CLI read stdin without an explicit stdin option")
		return foundationResult{}
	}
}

func foundationCLICommand(executable, directory string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	command := exec.Command(executable, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	return command, &stdout, &stderr
}

func foundationProcessResult(t *testing.T, stdout, stderr *bytes.Buffer, err error) foundationResult {
	t.Helper()
	if err == nil {
		return foundationResult{stdout: stdout.String(), stderr: stderr.String()}
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run compiled CLI: %v", err)
	}
	return foundationResult{stdout: stdout.String(), stderr: stderr.String(), exit: exitError.ExitCode()}
}

func assertFoundationResult(t *testing.T, got foundationResult, exit int, stdout, stderr string) {
	t.Helper()
	if got.exit != exit || got.stdout != stdout || got.stderr != stderr {
		t.Fatalf(
			"compiled CLI = exit %d stdout %q stderr %q, want exit %d stdout %q stderr %q",
			got.exit,
			got.stdout,
			got.stderr,
			exit,
			stdout,
			stderr,
		)
	}
}

func decodeFoundationSuccess[T any](t *testing.T, result foundationResult, command string) T {
	t.Helper()
	if result.exit != 0 || result.stderr != "" {
		t.Fatalf("%s = exit %d stdout %q stderr %q", command, result.exit, result.stdout, result.stderr)
	}
	var envelope foundationSuccess[T]
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatalf("decode %s output %q: %v", command, result.stdout, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != command {
		t.Fatalf("%s envelope = %#v", command, envelope)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if want := string(encoded) + "\n"; result.stdout != want {
		t.Fatalf("%s stdout = %q, want exact compact envelope %q", command, result.stdout, want)
	}
	return envelope.Data
}

func foundationErrorJSON(code, message string, details map[string]any) string {
	var envelope foundationError
	envelope.SchemaVersion = 1
	envelope.Error.Code = code
	envelope.Error.Message = message
	envelope.Error.Details = details
	encoded, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func createFoundationRepository(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runFoundationGit(t, root, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("foundation fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFoundationGit(t, root, "add", "tracked.txt")
	runFoundationGit(
		t,
		root,
		"-c", "user.name=Pellets Foundation Test",
		"-c", "user.email=pellets@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", "foundation fixture",
	)
}

func runFoundationGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	if output, err := foundationGitCommand(directory, args...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func foundationGitBytes(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	output, err := foundationGitCommand(directory, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func foundationGitText(t *testing.T, directory string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(foundationGitBytes(t, directory, args...)))
}

func foundationGitCommand(directory string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return command.CombinedOutput()
}

func foundationExcludePath(t *testing.T, root string) string {
	t.Helper()
	path := foundationGitText(t, root, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(path))
	}
	return filepath.Clean(path)
}

func appendFoundationExclude(before []byte) []byte {
	want := append([]byte(nil), before...)
	if len(want) > 0 && want[len(want)-1] != '\n' {
		want = append(want, '\n')
	}
	return append(want, []byte(".pellets/\n")...)
}

func assertFoundationGitState(t *testing.T, root, head string, index, status []byte) {
	t.Helper()
	if got := foundationGitText(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("Git HEAD changed from %q to %q", head, got)
	}
	if got := foundationGitBytes(t, root, "ls-files", "--stage", "-z"); !bytes.Equal(got, index) {
		t.Fatalf("Git index changed from %q to %q", index, got)
	}
	if got := foundationGitBytes(t, root, "status", "--porcelain=v1", "--untracked-files=all"); !bytes.Equal(got, status) {
		t.Fatalf("Git status changed from %q to %q", status, got)
	}
}

func assertFoundationDatabase(t *testing.T, databasePath string, projectCount int) {
	t.Helper()
	assertFoundationMetadataEntries(t, databasePath)
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}

	assertFoundationQueryInt(t, database, "PRAGMA user_version", sqlite.LatestSchemaVersion)
	assertFoundationQueryInt(t, database, "PRAGMA foreign_keys", 1)
	assertFoundationQueryInt(t, database, "PRAGMA trusted_schema", 0)
	assertFoundationQueryInt(t, database, "PRAGMA synchronous", 2)
	assertFoundationQueryInt(t, database, "PRAGMA busy_timeout", 5000)
	assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM projects", projectCount)
	assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM application_metadata", 3)
	assertFoundationQueryInt(t, database, `
		SELECT COUNT(*)
		FROM sqlite_schema
		WHERE type = 'table' AND lower(name) LIKE '%migration%'`, 0)
	assertFoundationQueryInt(t, database, `
		SELECT COUNT(*)
		FROM application_metadata
		WHERE lower(key) LIKE '%version%'`, 0)

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		database.Close()
		t.Fatalf("journal_mode = %q, want WAL", journalMode)
	}
	var ftsSource string
	if err := database.QueryRow("SELECT fts5_source_id()").Scan(&ftsSource); err != nil || ftsSource == "" {
		database.Close()
		t.Fatalf("FTS5 capability = %q, %v", ftsSource, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertFoundationMetadataEntries(t, databasePath)
}

func assertFoundationQueryInt(t *testing.T, database *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query result = %d, want %d\n%s", got, want, query)
	}
}

func assertFoundationMetadataEntries(t *testing.T, databasePath string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != discovery.DatabaseFilename || entries[0].IsDir() {
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		t.Fatalf("database metadata entries = %v, want only %s", names, discovery.DatabaseFilename)
	}
}

func assertFoundationTimestamp(t *testing.T, value string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || !strings.HasSuffix(value, "Z") {
		t.Fatalf("timestamp %q is not UTC RFC 3339: %v", value, err)
	}
}

func readFoundationFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func assertFoundationPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q exists or could not be inspected: %v", path, err)
	}
}

func foundationCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(absolute)
}
