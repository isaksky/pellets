package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type statusData struct {
	Ready bool `json:"ready"`
}

func (data statusData) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "ready\n")
	return err
}

func statusCommand() Command {
	return Command{
		Name:                  "status",
		Summary:               "Show harness status.",
		Usage:                 "pl status",
		SkipDatabaseDiscovery: true,
		Run: func(_ context.Context, _ Invocation) (any, error) {
			return statusData{Ready: true}, nil
		},
	}
}

func TestGoldenOutputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		app    *App
		args   []string
		exit   int
		stdout string
		stderr string
	}{
		{
			name:   "compact JSON",
			app:    NewWithCommands("test", statusCommand()),
			args:   []string{"status"},
			stdout: "compact.golden",
		},
		{
			name:   "pretty JSON",
			app:    NewWithCommands("test", statusCommand()),
			args:   []string{"--pretty", "status"},
			stdout: "pretty.golden",
		},
		{
			name:   "human",
			app:    NewWithCommands("test", statusCommand()),
			args:   []string{"--human", "status"},
			stdout: "human.golden",
		},
		{
			name:   "help",
			app:    New("test"),
			args:   []string{"--help"},
			stdout: "help.golden",
		},
		{
			name:   "version",
			app:    New("test"),
			args:   []string{"--version"},
			stdout: "version.golden",
		},
		{
			name:   "unknown command",
			app:    New("test"),
			args:   []string{"wat"},
			exit:   2,
			stderr: "unknown-command.golden",
		},
		{
			name:   "unknown global flag",
			app:    New("test"),
			args:   []string{"--wat"},
			exit:   2,
			stderr: "unknown-flag.golden",
		},
		{
			name:   "unknown command flag",
			app:    NewWithCommands("test", statusCommand()),
			args:   []string{"status", "--wat"},
			exit:   2,
			stderr: "unknown-flag.golden",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			exit := test.app.Run(test.args, &stdout, &stderr)
			if exit != test.exit {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", exit, test.exit, stdout.String(), stderr.String())
			}
			assertGolden(t, test.stdout, stdout.String())
			assertGolden(t, test.stderr, stderr.String())
			if test.exit == 0 && stderr.Len() != 0 {
				t.Fatalf("successful command wrote stderr: %q", stderr.String())
			}
			if test.exit != 0 && stdout.Len() != 0 {
				t.Fatalf("failed command wrote stdout: %q", stdout.String())
			}
		})
	}
}

func TestStrictGlobalParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		code string
	}{
		{"missing command", nil, "missing_command"},
		{"duplicate flag", []string{"--pretty", "--pretty"}, "duplicate_flag"},
		{"conflicting formats", []string{"--pretty", "--human", "status"}, "conflicting_flags"},
		{"conflicting terminal flags", []string{"--help", "--version"}, "conflicting_flags"},
		{"missing project", []string{"--project"}, "missing_flag_value"},
		{"argument after help", []string{"--help", "status"}, "unexpected_argument"},
		{"unexpected positional", []string{"status", "extra"}, "unexpected_argument"},
		{"global after command", []string{"status", "--pretty"}, "unknown_flag"},
	}
	app := NewWithCommands("test", statusCommand())
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if exit := app.Run(test.args, &stdout, &stderr); exit != 2 {
				t.Fatalf("exit = %d, want 2", exit)
			}
			if !bytes.Contains(stderr.Bytes(), []byte(`"code":"`+test.code+`"`)) {
				t.Fatalf("stderr = %q, want code %q", stderr.String(), test.code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestBrokenPipeIsQuiet(t *testing.T) {
	t.Parallel()

	app := NewWithCommands("test", statusCommand())
	var stderr bytes.Buffer
	exit := app.Run([]string{"status"}, failingWriter{}, &stderr)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1", exit)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	if name == "" {
		if got != "" {
			t.Fatalf("output = %q, want empty", got)
		}
		return
	}
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("output mismatch for %s\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}
