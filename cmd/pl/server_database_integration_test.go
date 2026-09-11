package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"pellets/internal/discovery"
)

func TestCompiledServerOutsideGit(t *testing.T) {
	executable := buildFoundationExecutable(t)
	for _, command := range []string{"server", "web"} {
		t.Run(command, func(t *testing.T) {
			for _, layout := range []string{"current", "ancestor", "empty"} {
				t.Run(layout, func(t *testing.T) {
					root := t.TempDir()
					decodeFoundationSuccess[map[string]any](t, runFoundationCLI(t, executable, root, "init-db"), "init-db")
					want := "No registered projects"
					if layout != "empty" {
						repo := filepath.Join(root, "registered")
						createFoundationRepository(t, repo)
						decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, repo, "add", "common parent task"), "add")
						want = "common parent task"
					}
					// An unregistered child must never be discovered or registered by startup.
					child := filepath.Join(root, "unregistered")
					createFoundationRepository(t, child)
					working := root
					if layout == "ancestor" {
						working = filepath.Join(root, "nested", "deeper")
						if err := os.MkdirAll(working, 0o755); err != nil {
							t.Fatal(err)
						}
					}
					address := startCompiledDatabaseServer(t, executable, working, command)
					response, err := (&http.Client{Timeout: 2 * time.Second}).Get(address)
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					response.Body.Close()
					if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), want) {
						t.Fatalf("GET = %d %q, error %v", response.StatusCode, body, err)
					}
					if strings.Contains(string(body), `workspace-chip current`) {
						t.Fatal("outside-Git server selected a current workspace")
					}
					projects := decodeFoundationSuccess[[]foundationProject](t, runFoundationCLI(t, executable, root, "project", "list"), "project list")
					count := 1
					if layout == "empty" {
						count = 0
					}
					if len(projects) != count {
						t.Fatalf("registered projects = %#v", projects)
					}
					if _, err := os.Stat(filepath.Join(child, ".git", "pellets-database.json")); !os.IsNotExist(err) {
						t.Fatalf("startup bound a child repository: %v", err)
					}
				})
			}
			t.Run("missing database does not scan children", func(t *testing.T) {
				root := t.TempDir()
				child := filepath.Join(root, "child")
				createFoundationRepository(t, child)
				decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, child, "add", "child task"), "add")
				result := runFoundationCLI(t, executable, root, command, "--no-open")
				assertFoundationErrorPath(t, result, 3, "database_not_found",
					"no Pellets database was found in the current directory or its ancestors; run inside a Git worktree or create a common-parent database with pl init-db",
					"start_path", foundationCanonicalPath(t, root))
				if _, err := os.Stat(discovery.DatabasePath(root)); !os.IsNotExist(err) {
					t.Fatalf("startup created an outside-Git database: %v", err)
				}
			})
		})
	}
}

// Start a real foreground process and always interrupt/reap it, including when
// a request assertion fails. Reading stderr waits until the process has exited.
func startCompiledDatabaseServer(t *testing.T, executable, directory, commandName string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot deliver os.Interrupt to a child process")
	}
	command := exec.Command(executable, commandName, "--port", "0", "--no-open")
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server exit: %v; stderr=%s", err, stderr.String())
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Errorf("server failed to shut down; stderr=%s", stderr.String())
		}
	})
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- strings.TrimSpace(line)
	}()
	select {
	case address := <-ready:
		if !strings.HasPrefix(address, "http://127.0.0.1:") {
			t.Fatalf("server readiness URL = %q", address)
		}
		return address
	case <-time.After(5 * time.Second):
		t.Fatal("server did not report readiness")
		return ""
	}
}
