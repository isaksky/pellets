package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"pellets/internal/discovery"
)

func TestCompiledCLIInitializationSafety(t *testing.T) {
	executable := buildTestExecutable(t)

	t.Run("normal initialization with spaces and Unicode is ignored", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "database root with spaces and 界")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		runCompiledGit(t, root, "init", "--quiet")

		stdout, stderr, exit := runCompiledCLI(t, executable, root, "init-db")
		databasePath := discovery.DatabasePath(root)
		if want := exactInitDBSuccess(databasePath); exit != 0 || stdout != want || stderr != "" {
			t.Fatalf("init-db = exit %d stdout %q stderr %q, want exit 0 stdout %q", exit, stdout, stderr, want)
		}
		entries, err := os.ReadDir(filepath.Dir(databasePath))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name() != discovery.DatabaseFilename || entries[0].IsDir() {
			t.Fatalf("metadata entries = %v", entryNames(entries))
		}
		if status := compiledGitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all"); len(status) != 0 {
			t.Fatalf("Git status after init-db = %q, want empty", status)
		}
		for _, path := range discovery.DatabasePaths(databasePath) {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				t.Fatal(err)
			}
			runCompiledGit(t, root, "check-ignore", "--quiet", "--no-index", "--", filepath.ToSlash(relative))
		}
	})

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		suffix := suffix
		t.Run("untracked companion "+suffix, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "untracked companion 界 "+strings.TrimPrefix(suffix, "-"))
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			runCompiledGit(t, root, "init", "--quiet")
			databasePath := discovery.DatabasePath(root)
			if err := os.Mkdir(filepath.Dir(databasePath), 0o755); err != nil {
				t.Fatal(err)
			}
			companionPath := databasePath + suffix
			sentinel := []byte("preexisting companion " + suffix)
			if err := os.WriteFile(companionPath, sentinel, 0o600); err != nil {
				t.Fatal(err)
			}
			excludePath := compiledExcludePath(t, root)
			excludeBefore := readCompiledTestFile(t, excludePath)
			indexBefore := compiledGitOutput(t, root, "ls-files", "--stage", "-z")

			stdout, stderr, exit := runCompiledCLI(t, executable, root, "init-db")
			wantError := exactCompanionConflict(databasePath, companionPath)
			if exit != 4 || stdout != "" || stderr != wantError {
				t.Fatalf("init-db = exit %d stdout %q stderr %q, want exit 4 stderr %q", exit, stdout, stderr, wantError)
			}
			if got := readCompiledTestFile(t, companionPath); !bytes.Equal(got, sentinel) {
				t.Fatalf("companion changed from %q to %q", sentinel, got)
			}
			assertCompiledPathAbsent(t, databasePath)
			assertCompiledStateUnchanged(t, root, excludePath, excludeBefore, indexBefore)
		})
	}

	for _, test := range []struct {
		name      string
		suffix    string
		indexOnly bool
	}{
		{name: "tracked WAL index-only", suffix: "-wal", indexOnly: true},
		{name: "tracked SHM with working file", suffix: "-shm"},
		{name: "tracked journal index-only", suffix: "-journal", indexOnly: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "tracked companion with spaces")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			runCompiledGit(t, root, "init", "--quiet")
			databasePath := discovery.DatabasePath(root)
			if err := os.Mkdir(filepath.Dir(databasePath), 0o755); err != nil {
				t.Fatal(err)
			}
			companionPath := databasePath + test.suffix
			sentinel := []byte("tracked sentinel " + test.suffix)
			if err := os.WriteFile(companionPath, sentinel, 0o600); err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(root, companionPath)
			if err != nil {
				t.Fatal(err)
			}
			runCompiledGit(t, root, "add", "-f", "--", filepath.ToSlash(relative))
			if test.indexOnly {
				if err := os.Remove(companionPath); err != nil {
					t.Fatal(err)
				}
			}
			excludePath := compiledExcludePath(t, root)
			excludeBefore := readCompiledTestFile(t, excludePath)
			indexBefore := compiledGitOutput(t, root, "ls-files", "--stage", "-z")
			statusBefore := compiledGitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all")

			stdout, stderr, exit := runCompiledCLI(t, executable, root, "init-db")
			wantError := exactTrackedConflict(databasePath)
			if exit != 4 || stdout != "" || stderr != wantError {
				t.Fatalf("init-db = exit %d stdout %q stderr %q, want exit 4 stderr %q", exit, stdout, stderr, wantError)
			}
			assertCompiledPathAbsent(t, databasePath)
			if test.indexOnly {
				assertCompiledPathAbsent(t, companionPath)
			} else if got := readCompiledTestFile(t, companionPath); !bytes.Equal(got, sentinel) {
				t.Fatalf("tracked companion changed from %q to %q", sentinel, got)
			}
			assertCompiledStateUnchanged(t, root, excludePath, excludeBefore, indexBefore)
			if statusAfter := compiledGitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all"); !bytes.Equal(statusAfter, statusBefore) {
				t.Fatalf("Git status changed from %q to %q", statusBefore, statusAfter)
			}
		})
	}

	t.Run("tracked case variant on a case-insensitive filesystem", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "case variant repository")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if !compiledTestCaseInsensitive(t, root) {
			t.Skip("filesystem is case-sensitive")
		}
		runCompiledGit(t, root, "init", "--quiet")
		uppercaseDirectory := filepath.Join(root, ".PELLETS")
		if err := os.Mkdir(uppercaseDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		uppercaseDatabase := filepath.Join(uppercaseDirectory, discovery.DatabaseFilename)
		if err := os.WriteFile(uppercaseDatabase, []byte("tracked uppercase database"), 0o600); err != nil {
			t.Fatal(err)
		}
		runCompiledGit(t, root, "add", "-f", "--", ".PELLETS/"+discovery.DatabaseFilename)
		if err := os.Remove(uppercaseDatabase); err != nil {
			t.Fatal(err)
		}
		databasePath := discovery.DatabasePath(root)
		excludePath := compiledExcludePath(t, root)
		excludeBefore := readCompiledTestFile(t, excludePath)
		indexBefore := compiledGitOutput(t, root, "ls-files", "--stage", "-z")
		statusBefore := compiledGitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all")

		stdout, stderr, exit := runCompiledCLI(t, executable, root, "init-db")
		wantError := exactTrackedConflict(databasePath)
		if exit != 4 || stdout != "" || stderr != wantError {
			t.Fatalf("init-db = exit %d stdout %q stderr %q, want exit 4 stderr %q", exit, stdout, stderr, wantError)
		}
		assertCompiledPathAbsent(t, databasePath)
		assertCompiledStateUnchanged(t, root, excludePath, excludeBefore, indexBefore)
		if statusAfter := compiledGitOutput(t, root, "status", "--porcelain=v1", "--untracked-files=all"); !bytes.Equal(statusAfter, statusBefore) {
			t.Fatalf("Git status changed from %q to %q", statusBefore, statusAfter)
		}
	})

	for _, targetLocation := range []string{"inside work tree", "outside work tree"} {
		targetLocation := targetLocation
		t.Run("symlink metadata "+targetLocation, func(t *testing.T) {
			temporary := t.TempDir()
			root := filepath.Join(temporary, "symlink repository with spaces")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			runCompiledGit(t, root, "init", "--quiet")
			target := filepath.Join(root, "inside metadata target")
			if targetLocation == "outside work tree" {
				target = filepath.Join(temporary, "outside metadata target 界")
			}
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			metadataPath := filepath.Join(root, discovery.MetadataDirectory)
			if err := os.Symlink(target, metadataPath); err != nil {
				if errors.Is(err, os.ErrPermission) || runtime.GOOS == "windows" {
					t.Skipf("symbolic links unavailable: %v", err)
				}
				t.Fatal(err)
			}
			excludePath := compiledExcludePath(t, root)
			excludeBefore := readCompiledTestFile(t, excludePath)
			indexBefore := compiledGitOutput(t, root, "ls-files", "--stage", "-z")

			stdout, stderr, exit := runCompiledCLI(t, executable, root, "init-db")
			wantError := exactMetadataSymlinkConflict(metadataPath)
			if exit != 4 || stdout != "" || stderr != wantError {
				t.Fatalf("init-db = exit %d stdout %q stderr %q, want exit 4 stderr %q", exit, stdout, stderr, wantError)
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("symlink target entries = %v, want empty", entryNames(entries))
			}
			linkTarget, err := os.Readlink(metadataPath)
			if err != nil || linkTarget != target {
				t.Fatalf("metadata symlink = %q, %v; want %q", linkTarget, err, target)
			}
			assertCompiledStateUnchanged(t, root, excludePath, excludeBefore, indexBefore)
		})
	}
}

func buildTestExecutable(t *testing.T) string {
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
	command := exec.Command("go", "build", "-o", executable, "./cmd/pl")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build compiled CLI: %v\n%s", err, output)
	}
	return executable
}

func runCompiledCLI(t *testing.T, executable, directory string, args ...string) (string, string, int) {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Dir = directory
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run compiled CLI: %v", err)
	}
	return stdout.String(), stderr.String(), exitError.ExitCode()
}

func exactInitDBSuccess(databasePath string) string {
	return `{"schema_version":1,"command":"init-db","data":{"database_path":` + compiledJSONString(databasePath) + "}}\n"
}

func exactCompanionConflict(databasePath, companionPath string) string {
	return `{"schema_version":1,"error":{"code":"database_companion_already_exists","message":"a SQLite companion path already exists for the Pellets database","details":{"companion_path":` + compiledJSONString(companionPath) + `,"database_path":` + compiledJSONString(databasePath) + "}}}\n"
}

func exactTrackedConflict(databasePath string) string {
	return `{"schema_version":1,"error":{"code":"database_already_tracked","message":"the Pellets database path is already tracked by Git","details":{"database_path":` + compiledJSONString(databasePath) + "}}}\n"
}

func exactMetadataSymlinkConflict(metadataPath string) string {
	return `{"schema_version":1,"error":{"code":"database_metadata_symlink","message":"the Pellets metadata directory must not be a symbolic link","details":{"metadata_path":` + compiledJSONString(metadataPath) + "}}}\n"
}

func compiledJSONString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func runCompiledGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func compiledGitOutput(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return output
}

func compiledExcludePath(t *testing.T, root string) string {
	t.Helper()
	path := strings.TrimSpace(string(compiledGitOutput(t, root, "rev-parse", "--git-path", "info/exclude")))
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(path))
	}
	return filepath.Clean(path)
}

func readCompiledTestFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func assertCompiledPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q after rejection: %v", path, err)
	}
}

func assertCompiledStateUnchanged(t *testing.T, root, excludePath string, excludeBefore, indexBefore []byte) {
	t.Helper()
	if excludeAfter := readCompiledTestFile(t, excludePath); !bytes.Equal(excludeAfter, excludeBefore) {
		t.Fatalf("local exclude changed from %q to %q", excludeBefore, excludeAfter)
	}
	if indexAfter := compiledGitOutput(t, root, "ls-files", "--stage", "-z"); !bytes.Equal(indexAfter, indexBefore) {
		t.Fatalf("Git index changed from %q to %q", indexBefore, indexAfter)
	}
}

func compiledTestCaseInsensitive(t *testing.T, root string) bool {
	t.Helper()
	lowercase := filepath.Join(root, "case-probe")
	if err := os.WriteFile(lowercase, []byte("probe"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, upperErr := os.Lstat(filepath.Join(root, "CASE-PROBE"))
	if err := os.Remove(lowercase); err != nil {
		t.Fatal(err)
	}
	return upperErr == nil
}
