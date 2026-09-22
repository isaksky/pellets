package main

import (
	"bufio"
	"bytes"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCompiledServerDiscoveryStartupAndCleanShutdown(t *testing.T) {
	testCompiledServerStartup(t, false)
}

func TestCompiledServerDefaultSkipsOccupiedPort(t *testing.T) {
	testCompiledServerStartup(t, true)
}

func testCompiledServerStartup(t *testing.T, defaultPort bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot deliver os.Interrupt to a child process; Windows build coverage is in verify-cross-builds.sh")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("compiled server test requires Git: %v", err)
	}
	executable := buildFoundationExecutable(t)
	root := filepath.Join(t.TempDir(), "webtest")
	createFoundationRepository(t, root)
	nested := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	arguments := []string{"server", "--port", "0", "--no-open"}
	if defaultPort {
		occupied, err := net.Listen("tcp4", "127.0.0.1:7419")
		if err != nil {
			t.Skipf("cannot reserve default port for collision test: %v", err)
		}
		defer occupied.Close()
		arguments = []string{"server", "--no-open"}
	}
	command := exec.Command(executable, arguments...)
	command.Dir = nested
	// Eager catalog warming must never discover the developer's installed runtime.
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "PELLETS_CODEX_EXECUTABLE="+filepath.Join(root, "unavailable-codex"))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	line := make(chan string, 1)
	readErrors := make(chan error, 1)
	go func() {
		value, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			readErrors <- err
			return
		}
		line <- strings.TrimSpace(value)
	}()
	var address string
	select {
	case address = <-line:
	case err := <-readErrors:
		t.Fatalf("read server URL: %v; stderr=%s", err, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("compiled server command did not report readiness")
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		t.Fatalf("reported URL = %q, parse error %v", address, err)
	}
	if defaultPort {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port <= 7419 {
			t.Fatalf("fallback URL = %q, expected port above 7419", address)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".pellets", "pellets.db")); err != nil {
		t.Fatalf("first-use server did not bootstrap the project database: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "pellets-database.json")); err != nil {
		t.Fatalf("first-use server did not bind the project database: %v", err)
	}
	projects := decodeFoundationSuccess[[]foundationProject](t, runFoundationCLI(t, executable, root, "project", "list"), "project list")
	if len(projects) != 1 || projects[0].Code != "webtest" || len(projects[0].Workspaces) != 1 {
		t.Fatalf("first-use server registration = %#v", projects)
	}
	result := runFoundationCLI(t, executable, root, "add", "compiled server task")
	if result.exit != 0 || result.stderr != "" {
		t.Fatalf("add = exit %d stdout %q stderr %q", result.exit, result.stdout, result.stderr)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(address + "/projects/webtest/tasks")
	if err != nil {
		t.Fatalf("GET ready server: %v", err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), "compiled server task") {
		t.Fatalf("GET = %d %q", response.StatusCode, body.String())
	}
	// Keep the browser's persistent stream open while interrupting the server.
	stream, err := http.Get(address + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d", stream.StatusCode)
	}
	if _, err := bufio.NewReader(stream.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatalf("compiled server shutdown = %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(time.Second):
		t.Fatal("compiled server command did not shut down after interrupt")
	}
	var lifecycleOutput strings.Builder
	for _, line := range strings.SplitAfter(stderr.String(), "\n") {
		if strings.HasPrefix(line, "model catalog refresh: duration=") {
			continue
		}
		lifecycleOutput.WriteString(line)
	}
	if lifecycleOutput.String() != "Stopping server… Press Ctrl+C again to force exit.\n" {
		t.Fatalf("compiled --no-open stderr = %q", stderr.String())
	}

	invalid := runFoundationCLI(t, executable, root, "server", "--port", "080", "--no-open")
	if invalid.exit != 2 || !strings.HasPrefix(invalid.stderr, "Error:") || !strings.Contains(invalid.stderr, "pl server --help") || invalid.stdout != "" {
		t.Fatalf("invalid port = exit %d stdout %q stderr %q", invalid.exit, invalid.stdout, invalid.stderr)
	}

	alias := runFoundationCLI(t, executable, root, "web", "--port", "080", "--no-open")
	if alias.exit != 2 || !strings.HasPrefix(alias.stderr, "Error:") || !strings.Contains(alias.stderr, "pl server --help") || alias.stdout != "" {
		t.Fatalf("web compatibility alias invalid port = exit %d stdout %q stderr %q", alias.exit, alias.stdout, alias.stderr)
	}
}
