package main

import (
	"bufio"
	"bytes"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCompiledWebDiscoveryStartupAndCleanShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot deliver os.Interrupt to a child process; Windows build coverage is in verify-cross-builds.sh")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("compiled web test requires Git: %v", err)
	}
	executable := buildFoundationExecutable(t)
	root := filepath.Join(t.TempDir(), "web compiled 界")
	createFoundationRepository(t, root)
	result := runFoundationCLI(t, executable, root, "init", "--code", "webtest")
	if result.exit != 0 || result.stderr != "" {
		t.Fatalf("init = exit %d stdout %q stderr %q", result.exit, result.stdout, result.stderr)
	}
	result = runFoundationCLI(t, executable, root, "add", "compiled web task")
	if result.exit != 0 || result.stderr != "" {
		t.Fatalf("add = exit %d stdout %q stderr %q", result.exit, result.stdout, result.stderr)
	}
	nested := filepath.Join(root, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(executable, "web", "--port", "0", "--no-open")
	command.Dir = nested
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
		t.Fatalf("read web URL: %v; stderr=%s", err, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("compiled web command did not report readiness")
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		t.Fatalf("reported URL = %q, parse error %v", address, err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(address + "/projects/webtest/tasks")
	if err != nil {
		t.Fatalf("GET ready web server: %v", err)
	}
	body := new(bytes.Buffer)
	_, _ = body.ReadFrom(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), "compiled web task") {
		t.Fatalf("GET = %d %q", response.StatusCode, body.String())
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
			t.Fatalf("compiled web shutdown = %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(7 * time.Second):
		t.Fatal("compiled web command did not shut down after interrupt")
	}
	if stderr.Len() != 0 {
		t.Fatalf("compiled --no-open stderr = %q", stderr.String())
	}

	invalid := runFoundationCLI(t, executable, root, "web", "--port", "080", "--no-open")
	if invalid.exit != 2 || !strings.Contains(invalid.stderr, `"code":"invalid_port"`) || invalid.stdout != "" {
		t.Fatalf("invalid port = exit %d stdout %q stderr %q", invalid.exit, invalid.stdout, invalid.stderr)
	}
}
