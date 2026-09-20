package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/storage"
)

func TestCompiledGroupContextFilesStdinAndNoninteractiveOutput(t *testing.T) {
	executable := buildFoundationExecutable(t)
	root := filepath.Join(t.TempDir(), "group-cli")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if output, err := foundationGitCommand(root, "init", "-q"); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	help := runFoundationCLI(t, executable, root, "--help")
	if help.exit != 0 || !strings.Contains(help.stdout, "group        Create, list, show, edit, or rename") {
		t.Fatalf("group missing from help: %+v", help)
	}
	markdown := "# Markdown Ω\r\n\n```mermaid\nflowchart LR\n\tA --> B\n```\n\n`backticks` $literal\n"
	if err := os.WriteFile(filepath.Join(root, "group context.md"), []byte(markdown), 0600); err != nil {
		t.Fatal(err)
	}
	g := decodeCompiledGroup(t, runFoundationCLIWithBlockedStdin(t, executable, root, "group", "create", "file", "--context-file", "group context.md"), "group create")
	if g.Context != markdown || g.Revision != 1 {
		t.Fatalf("file roundtrip: %+v", g)
	}
	id := strconv.FormatInt(g.ID, 10)
	for _, action := range [][]string{{"group", "create", "stdin"}, {"group", "edit", id}} {
		cmd, stdout, stderr := foundationCLICommand(executable, root, append(action, "--context-file", "-")...)
		cmd.Stdin = strings.NewReader("yes\n" + markdown)
		result := foundationProcessResult(t, stdout, stderr, cmd.Run())
		got := decodeCompiledGroup(t, result, "group "+action[1])
		if got.Context != "yes\n"+markdown {
			t.Fatalf("stdin consumed or changed: %q", got.Context)
		}
	}
	shown := decodeCompiledGroup(t, runFoundationCLIWithBlockedStdin(t, executable, root, "group", "show", id), "group show")
	if shown.Context != "yes\n"+markdown || shown.Revision != 2 {
		t.Fatal(shown)
	}
	cleared := decodeCompiledGroup(t, runFoundationCLIWithBlockedStdin(t, executable, root, "group", "edit", id, "--clear-context", "--revision", "2"), "group edit")
	if cleared.Context != "" || cleared.Revision != 3 {
		t.Fatal(cleared)
	}
	stale := runFoundationCLIWithBlockedStdin(t, executable, root, "group", "edit", id, "--context", "lost", "--revision", "2")
	if stale.exit != 4 || stale.stdout != "" || !strings.Contains(stale.stderr, "group_revision_conflict") {
		t.Fatalf("stale: %+v", stale)
	}
	duplicate := runFoundationCLIWithBlockedStdin(t, executable, root, "group", "create", "file")
	if duplicate.exit != 4 || duplicate.stdout != "" || !strings.Contains(duplicate.stderr, "group_name_conflict") {
		t.Fatalf("duplicate: %+v", duplicate)
	}
}

func decodeCompiledGroup(t *testing.T, result foundationResult, command string) storage.Group {
	t.Helper()
	if result.exit != 0 || result.stderr != "" {
		t.Fatalf("%s: %+v", command, result)
	}
	var envelope foundationSuccess[storage.Group]
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != command {
		t.Fatalf("envelope: %+v", envelope)
	}
	// Check compact output without re-marshalling the storage model: public field
	// order differs and the CLI deliberately does not HTML-escape Mermaid arrows.
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(result.stdout)); err != nil {
		t.Fatal(err)
	}
	if result.stdout != compact.String()+"\n" {
		t.Fatalf("noncompact output: %q", result.stdout)
	}
	return envelope.Data
}
