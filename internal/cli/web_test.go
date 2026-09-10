package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"pellets/internal/discovery"
	"pellets/internal/domain"
)

func TestParseServerOptionsStrictly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		args []string
		want WebOptions
		code string
	}{
		{name: "defaults", want: WebOptions{}},
		{name: "free port explicit", args: []string{"--port", "0", "--no-open"}, want: WebOptions{NoOpen: true}},
		{name: "fixed port equals", args: []string{"--port=8123"}, want: WebOptions{Port: 8123}},
		{name: "duplicate", args: []string{"--no-open", "--no-open"}, code: "duplicate_flag"},
		{name: "unknown", args: []string{"--listen", "0.0.0.0"}, code: "unknown_flag"},
		{name: "noncanonical", args: []string{"--port", "080"}, code: "invalid_port"},
		{name: "overflow", args: []string{"--port", "65536"}, code: "invalid_port"},
		{name: "positional", args: []string{"extra"}, code: "unexpected_argument"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseServerOptions(test.args)
			if test.code != "" {
				if err == nil || publicCode(err) != test.code {
					t.Fatalf("parseServerOptions() error = %v, want %s", err, test.code)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(parsed, test.want) {
				t.Fatalf("parseServerOptions() = (%#v, %v), want %#v", parsed, err, test.want)
			}
		})
	}
}

func TestServerCommandForegroundOwnsOutputAndRejectsFormatGlobals(t *testing.T) {
	t.Parallel()
	var got WebOptions
	command := ServerCommand(func(_ context.Context, invocation Invocation, options ServerOptions, stdout, stderr io.Writer) error {
		got = options
		_, _ = io.WriteString(stdout, "http://127.0.0.1:8123\n")
		_, _ = io.WriteString(stderr, "warning: test\n")
		if invocation.Globals.Project != "demo" {
			t.Fatalf("project = %q", invocation.Globals.Project)
		}
		return nil
	})
	if command.Run != nil || command.RunForeground == nil {
		t.Fatal("server command did not use the foreground boundary")
	}
	if err := command.Validate(GlobalOptions{Pretty: true}, WebOptions{}); err == nil || publicCode(err) != "format_not_supported" {
		t.Fatalf("pretty validation error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	err := command.RunForeground(context.Background(), Invocation{
		Globals: GlobalOptions{Project: "demo"}, Input: WebOptions{Port: 8123, NoOpen: true},
	}, &stdout, &stderr)
	if err != nil || got != (WebOptions{Port: 8123, NoOpen: true}) {
		t.Fatalf("RunForeground() = (%#v, %v)", got, err)
	}
	if stdout.String() != "http://127.0.0.1:8123\n" || stderr.String() != "warning: test\n" {
		t.Fatalf("foreground output = stdout %q stderr %q", stdout.String(), stderr.String())
	}
}

func TestWebIsCompatibilityAliasForCanonicalServerCommand(t *testing.T) {
	t.Parallel()
	command := ServerCommand(func(_ context.Context, _ Invocation, _ ServerOptions, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, "http://127.0.0.1:8123\n")
		return err
	})
	application := New("test", command).WithCurrentWorkspaceBootstrap(func(context.Context, string) (discovery.Database, error) {
		return discovery.Database{}, nil
	})
	workingDirectory := t.TempDir()
	application.workingDirectory = func() (string, error) { return workingDirectory, nil }

	stdout, stderr, exit := runTestApp(application, "web", "--port", "8123", "--no-open")
	if exit != 0 || stderr != "" || stdout != "http://127.0.0.1:8123\n" {
		t.Fatalf("web alias = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "web", "--help")
	if exit != 0 || stderr != "" || stdout != "Usage:\n  pl [--project CODE] server [--port PORT] [--no-open]\n" {
		t.Fatalf("web alias help = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "--help")
	if exit != 0 || stderr != "" || !strings.Contains(stdout, "  server       Run the foreground local server.\n") || strings.Contains(stdout, "  web") {
		t.Fatalf("canonical help = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
}

func publicCode(err error) string {
	if err == nil {
		return ""
	}
	return domain.PublicError(err).Code
}

func TestServerInterruptHelper(t *testing.T) {
	if os.Getenv("PELLETS_TEST_WEB_INTERRUPT") != "1" {
		return
	}
	command := ServerCommand(func(ctx context.Context, _ Invocation, _ ServerOptions, stdout, _ io.Writer) error {
		fmt.Fprintln(stdout, "ready")
		<-ctx.Done()
		fmt.Fprintln(stdout, "draining")
		time.Sleep(time.Hour) // Simulate shutdown that cannot finish gracefully.
		return nil
	})
	_ = command.RunForeground(context.Background(), Invocation{Input: WebOptions{}}, os.Stdout, os.Stderr)
}

func TestServerSecondInterruptForcesExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot send os.Interrupt to a child process")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestServerInterruptHelper$")
	command.Env = append(os.Environ(), "PELLETS_TEST_WEB_INTERRUPT=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer command.Process.Kill()
	lines := func(reader io.Reader) <-chan string {
		result := make(chan string, 4)
		go func() {
			defer close(result)
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				result <- scanner.Text()
			}
		}()
		return result
	}
	output, feedback := lines(stdout), lines(stderr)
	expect := func(ch <-chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	expect(output, "ready")
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	expect(feedback, "Stopping server… Press Ctrl+C again to force exit.")
	expect(output, "draining")
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupt") {
			t.Fatalf("force exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second interrupt did not force exit")
	}
}
