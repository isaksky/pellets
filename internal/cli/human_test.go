package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRawApp(a *App, args ...string) (string, string, int) {
	var out, err bytes.Buffer
	code := a.Run(args, &out, &err)
	return out.String(), err.String(), code
}

func TestDefaultHumanAndExplicitMachinePolicy(t *testing.T) {
	a := New("test", statusCommand())
	a.stdin = errorReader{}
	for _, interactive := range []bool{false, true} {
		a.isInteractive = func(io.Reader, io.Writer) bool { return interactive }
		for _, args := range [][]string{{"status"}, {"--human", "status"}} {
			out, err, code := runRawApp(a, args...)
			if code != 0 || err != "" || out != "ready\n" {
				t.Fatalf("%v: %d %q %q", args, code, out, err)
			}
		}
		for _, args := range [][]string{{"--json", "status"}, {"--pretty", "status"}, {"--json", "--pretty", "status"}} {
			out, err, code := runRawApp(a, args...)
			if code != 0 || err != "" || !json.Valid([]byte(out)) {
				t.Fatalf("%v: %d %q %q", args, code, out, err)
			}
		}
		for _, args := range [][]string{{"--json", "--unknown"}, {"--unknown", "--json"}, {"--pretty", "--unknown"}, {"--json", "--human", "status"}, {"--human", "--json", "status"}, {"--json", "--json", "status"}, {"--project", "--json", "status"}} {
			out, err, code := runRawApp(a, args...)
			if code != 2 || out != "" || !json.Valid([]byte(err)) {
				t.Fatalf("%v: %d %q %q", args, code, out, err)
			}
		}
	}
	for _, args := range [][]string{nil, {"unknown"}, {"status", "--json"}, {"--human", "--unknown"}, {"--json=true"}} {
		out, err, code := runRawApp(a, args...)
		if code != 2 || out != "" {
			t.Fatalf("%v: %d %q %q", args, code, out, err)
		}
		if args != nil && args[0] == "--json=true" {
			if !json.Valid([]byte(err)) {
				t.Fatal(err)
			}
			continue
		}
		if !strings.HasPrefix(err, "Error:") || json.Valid([]byte(err)) {
			t.Fatalf("human error: %q", err)
		}
	}
	out, err, code := runRawApp(a, "status", "--description", "--json")
	if code != 2 || out != "" || !strings.HasPrefix(err, "Error:") {
		t.Fatalf("payload selected JSON: %q", err)
	}
}

func TestHumanRecordsAndPayloadStdin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "demo")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "init", "-q")
	a := projectTestApp(&root)
	a.stdin = errorReader{}
	check := func(args []string, wants ...string) string {
		t.Helper()
		out, err, code := runRawApp(a, args...)
		if code != 0 || err != "" || json.Valid([]byte(out)) || strings.Contains(out, "\x1b") {
			t.Fatalf("%v: %d %q %q", args, code, out, err)
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Fatalf("%q missing from %q", want, out)
			}
		}
		return out
	}
	check([]string{"list"}, "No pellets.")
	title := strings.Repeat("complete title 界 ", 20)
	description := "yes\n" + strings.Repeat("full description ", 30)
	a.stdin = strings.NewReader(description)
	check([]string{"add", title, "--description-file", "-", "--external-id", "source", "--group", "group"}, "demo-1", title)
	a.stdin = errorReader{}
	check([]string{"show", "demo-1"}, title, description, "source", "group", "created at:", "updated at:")
	check([]string{"list"}, title)
	a.stdin = strings.NewReader("yes\ncomplete memory\n")
	check([]string{"memory", "add", "--file", "-"}, "Added memory 1 to demo", "unapproved")
	a.stdin = errorReader{}
	check([]string{"memory", "show", "1"}, "yes\ncomplete memory", "created by:")
	check([]string{"memory", "search", "complete"}, "yes\ncomplete memory")
	for _, args := range [][]string{{"memory", "remove", "1"}, {"purge", "--project", "demo"}, {"release", "demo-1", "--recover-workspace", "1"}} {
		out, err, code := runRawApp(a, args...)
		if code != 6 || out != "" || !strings.Contains(err, "pl --json") || !strings.Contains(err, "--yes") {
			t.Fatalf("%v: %d %q %q", args, code, out, err)
		}
	}
}

func TestConfirmationEOFDoesNotApprovePartialAnswer(t *testing.T) {
	for _, answer := range []string{"", "yes", "y", "\x03", "yes\x04"} {
		ok, err := newInteraction(strings.NewReader(answer), io.Discard).confirm("Continue? ")
		if err != nil || ok {
			t.Fatalf("%q approved: %v, %v", answer, ok, err)
		}
	}
}

func TestNullDeviceIsNotAnInteractiveTerminal(t *testing.T) {
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if streamsAreInteractive(input, output) {
		t.Fatal("character devices are not necessarily terminals")
	}
}
