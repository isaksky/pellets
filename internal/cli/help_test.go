package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"pellets/internal/app"
)

func helpTestApp() *App {
	a := New("test", AddCommand(app.PelletManager{}), ShowCommand(app.PelletManager{}),
		ListCommand(app.PelletManager{}), MemoryCommand(app.MemoryManager{}),
		ProjectCommand(app.ProjectManager{}), GroupCommand(app.GroupManager{}),
		SkillCommand(app.SkillInstaller{}))
	a.workingDirectory = func() (string, error) { panic("help or invalid input attempted discovery") }
	a.stdin = errorReader{}
	return a
}

func TestHelpDiscoveryDoesNotReadStdinOrDiscoverDatabase(t *testing.T) {
	a := helpTestApp()
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}, {"--human"}, {"--json", "--help"}} {
		out, err, code := runRawApp(a, args...)
		if code != 0 || err != "" || !strings.Contains(out, "pl list") || !strings.Contains(out, "pl help <command>") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, code, out, err)
		}
	}
	for _, args := range [][]string{{"add", "--help"}, {"add", "-h"}, {"help", "add"}} {
		out, err, code := runRawApp(a, args...)
		for _, want := range []string{"Add a pellet", "Options:", "Examples:", "- reads stdin", "--description-file details.md"} {
			if code != 0 || err != "" || !strings.Contains(out, want) {
				t.Fatalf("%v: want %q; exit %d, stdout %q, stderr %q", args, want, code, out, err)
			}
		}
		for _, line := range strings.Split(out, "\n") {
			if len(line) > 88 {
				t.Fatalf("unreadable add help line: %q", line)
			}
		}
	}
	for _, family := range []struct{ name, sub string }{{"group", "create"}, {"memory", "add"}, {"project", "rename"}, {"skill", "install"}} {
		for _, args := range [][]string{{family.name}, {family.name, family.sub, "--help"}, {family.name, family.sub, "-h"}, {"help", family.name, family.sub}} {
			out, err, code := runRawApp(a, args...)
			if code != 0 || err != "" || !strings.Contains(out, "Examples") || !strings.Contains(out, "Usage:") {
				t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, code, out, err)
			}
		}
	}
}

func TestHumanErrorsExplainHowToProceed(t *testing.T) {
	a := helpTestApp()
	for _, test := range []struct {
		args []string
		want string
		omit string
	}{
		{[]string{"show"}, "pl show foo-123", "missing_reference"},
		{[]string{"wat"}, "pl --help", "command: wat"},
		{[]string{"--wat"}, "pl --help", "flag: --wat"},
		{[]string{"add", "--wat"}, "pl add --help", "unknown_flag"},
		{[]string{"list", "--json"}, "pl --json list", "unknown_flag"},
		{[]string{"list", "--pretty"}, "pl --pretty list", "unknown_flag"},
		{[]string{"list", "--project", "foo"}, "pl --project CODE list", "unknown_flag"},
		{[]string{"help", "memory", "wat"}, "pl --help", "unexpected_argument"},
	} {
		out, err, code := runRawApp(a, test.args...)
		if code != 2 || out != "" || !strings.Contains(err, test.want) || strings.Contains(err, test.omit) {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", test.args, code, out, err)
		}
	}
}

func TestHelpPreservesMachineErrorsAndLiteralPayloads(t *testing.T) {
	a := helpTestApp()
	for _, args := range [][]string{{"--json"}, {"--pretty"}, {"--json", "show"}, {"--json", "memory"}, {"--json", "list", "--pretty"}} {
		out, err, code := runRawApp(a, args...)
		if code != 2 || out != "" || !json.Valid([]byte(err)) || strings.Contains(err, "Run 'pl") {
			t.Fatalf("%v: exit %d, stdout %q, stderr %q", args, code, out, err)
		}
	}
	// Help and format-looking strings remain payloads, never global options.
	for _, value := range []string{"-h", "--help", "--json", "help"} {
		parsed, err := a.parse([]string{"add", "title", "--description=" + value})
		if err != nil || parsed.action != actionRun || parsed.globals.machine() {
			t.Fatalf("payload %q changed dispatch: %+v, %v", value, parsed, err)
		}
		input, err := parsed.command.Parse(parsed.args)
		if err != nil || input.(addInput).Description == nil || *input.(addInput).Description != value {
			t.Fatalf("payload %q was changed: %+v, %v", value, input, err)
		}
	}
}
