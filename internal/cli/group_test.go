package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pellets/internal/app"
	"pellets/internal/discovery"
	"pellets/internal/output"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func groupTestApp(t *testing.T) (*App, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "groups")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "init", "-q")
	return projectTestApp(&root), root
}

func runGroup(t *testing.T, a *App, args ...string) groupData {
	t.Helper()
	raw := runPelletCommand(t, a, args...)
	var envelope struct {
		Data groupData `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestGroupCLIContextLifecycleAndRouting(t *testing.T) {
	t.Parallel()
	a, root := groupTestApp(t)
	a.stdin = errorReader{}
	g := runGroup(t, a, "group", "create", "parser")
	if g.Context != "" || g.Revision != 1 || g.ID <= 0 {
		t.Fatalf("empty group: %+v", g)
	}
	id := strconv.FormatInt(g.ID, 10)
	markdown := "# Shared Ω\r\n\n```mermaid\nflowchart LR\n\tA --> B\n```\n\n$literal `shell`\n"
	if err := os.WriteFile(filepath.Join(root, "group context.md"), []byte(markdown), 0600); err != nil {
		t.Fatal(err)
	}
	fromFile := runGroup(t, a, "group", "create", "from-file", "--context-file", "group context.md")
	if fromFile.Context != markdown || fromFile.Revision != 1 {
		t.Fatalf("file create: %+v", fromFile)
	}
	g = runGroup(t, a, "group", "edit", id, "--context-file", "group context.md", "--revision", "1")
	if g.Context != markdown || g.Revision != 2 {
		t.Fatalf("file edit: %+v", g)
	}
	if shown := runGroup(t, a, "group", "show", id); shown != g {
		t.Fatalf("show changed context: %+v", shown)
	}
	for _, args := range [][]string{
		{"group", "edit", id, "--context", "lost", "--revision", "1"},
		{"group", "rename", id, "lost-name", "--revision", "1"},
	} {
		err := runPelletError(t, a, 4, args...)
		for _, want := range []string{"group_revision_conflict", `"revision":2`, `"expected_revision":1`, `"reload_argv":["pl","--json","group","show","` + id + `"]`} {
			if !strings.Contains(err, want) {
				t.Fatalf("stale diagnostic missing %s: %s", want, err)
			}
		}
	}
	for _, args := range [][]string{
		{"group", "create", "parser"},
		{"group", "create", "parser", "--context", "replacement"},
		{"group", "rename", id, "from-file"},
	} {
		if err := runPelletError(t, a, 4, args...); !strings.Contains(err, "group_name_conflict") {
			t.Fatal(err)
		}
	}
	runPelletError(t, a, 2, "group", "edit", id)
	if got := runGroup(t, a, "group", "show", id); got != g {
		t.Fatalf("failed edits changed group: %+v", got)
	}
	// A payload beginning with an affirmative answer is still entirely context,
	// including when terminal detection says an interactive prompt is possible.
	a.isInteractive = func(io.Reader, io.Writer) bool { return true }
	a.stdin = strings.NewReader("yes\n" + markdown)
	out, errText, exit := runRawApp(a, "group", "edit", id, "--context-file", "-")
	if exit != 0 || errText != "" || !strings.Contains(out, "yes\n") || strings.Contains(out, "[y/N]") {
		t.Fatalf("stdin edit: %d %q %q", exit, out, errText)
	}
	a.stdin = errorReader{}
	g = runGroup(t, a, "group", "show", id)
	if g.Context != "yes\n"+markdown {
		t.Fatalf("consumed payload: %q", g.Context)
	}
	for _, flags := range [][]string{{"--clear-context"}, {"--context", ""}, {"--context-file", "-"}} {
		runGroup(t, a, "group", "edit", id, "--context", markdown)
		a.stdin = strings.NewReader("")
		g = runGroup(t, a, append([]string{"group", "edit", id}, flags...)...)
		if g.Context != "" {
			t.Fatalf("clear %v: %+v", flags, g)
		}
	}
	a.stdin = errorReader{}
	g = runGroup(t, a, "group", "edit", id, "--context", markdown)
	runPelletCommand(t, a, "add", "member", "--group", "parser")
	ctx := context.Background()
	projects, err := sqlite.OpenProjectDatabase(ctx, discovery.DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer projects.Close()
	project, err := projects.FindProjectByCode(ctx, "groups")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sqlite.OpenWebReader(ctx, discovery.DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := sqlite.OpenWebWriter(ctx, discovery.DatabasePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	routing, err := reader.ReadProjectRouting(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	routing, err = writer.SaveWorkspaceAssignment(ctx, project, project.Workspaces[0].ID, routing.Version, storage.WorkspaceAssignment{Mode: "explicit", Groups: []string{"parser"}})
	if err != nil {
		t.Fatal(err)
	}
	renamed := runGroup(t, a, "group", "rename", id, "renamed", "--revision", strconv.FormatInt(g.Revision, 10))
	if renamed.ID != g.ID || renamed.Context != markdown || renamed.Revision != g.Revision+1 {
		t.Fatalf("rename: %+v", renamed)
	}
	member := runPelletCommand(t, a, "show", "groups-1")
	if !strings.Contains(member, `"group":"renamed"`) {
		t.Fatalf("lost member: %s", member)
	}
	if list := runPelletCommand(t, a, "list", "--group", "renamed"); !strings.Contains(list, "groups-1") {
		t.Fatal(list)
	}
	updated, err := reader.ReadProjectRouting(ctx, project)
	if err != nil || updated.Version == routing.Version || !reflect.DeepEqual(updated.Assignment(project.Workspaces[0].ID).Groups, []string{"renamed"}) {
		t.Fatalf("routing: %+v %v", updated, err)
	}
}

func TestGroupCLIProjectsAndOutputModes(t *testing.T) {
	t.Parallel()
	common := t.TempDir()
	runPelletCommand(t, initDBTestApp(common), "init-db")
	current := common
	a := projectTestApp(&current)
	groups := []groupData{}
	for _, name := range []string{"alpha", "beta"} {
		current = filepath.Join(common, name)
		if err := os.Mkdir(current, 0755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, current, "init", "-q")
		groups = append(groups, runGroup(t, a, "group", "create", "same", "--context", name))
	}
	if groups[0].ID == groups[1].ID || groups[0].ProjectID == groups[1].ProjectID {
		t.Fatal(groups)
	}
	runPelletError(t, a, 3, "group", "show", strconv.FormatInt(groups[0].ID, 10))
	current = common // Explicit project commands also work outside Git.
	runPelletCommand(t, a, "--project", "alpha", "project", "rename", "canonical")
	id := strconv.FormatInt(groups[0].ID, 10)
	if got := runGroup(t, a, "--project", "alpha", "group", "show", id); got != groups[0] {
		t.Fatal(got)
	}
	g := runGroup(t, a, "--project", "canonical", "group", "edit", id, "--context", "changed", "--revision", "1")
	if g.Context != "changed" {
		t.Fatal(g)
	}
	if got := runGroup(t, a, "--project", "beta", "group", "show", strconv.FormatInt(groups[1].ID, 10)); got != groups[1] {
		t.Fatal(got)
	}
	a.stdin = errorReader{}
	for _, terminal := range []bool{false, true} {
		a.isInteractive = func(io.Reader, io.Writer) bool { return terminal }
		for _, format := range [][]string{nil, {"--human"}, {"--json"}, {"--pretty"}} {
			args := append(append([]string{}, format...), "--project", "canonical", "group", "list")
			out, diagnostics, exit := runRawApp(a, args...)
			if exit != 0 || diagnostics != "" || strings.Contains(out, "changed") || strings.Contains(out, "context") || strings.Contains(out, "[y/N]") {
				t.Fatalf("list %v: %d %q %q", args, exit, out, diagnostics)
			}
			machine := len(format) > 0 && (format[0] == "--json" || format[0] == "--pretty")
			if json.Valid([]byte(out)) != machine {
				t.Fatalf("format %v: %s", format, out)
			}
		}
	}
}

func TestGroupCLIRejectsInvalidUsageBeforeDiscoveryOrStdin(t *testing.T) {
	a := New("test", GroupCommand(app.GroupManager{}))
	a.stdin = errorReader{}
	a.workingDirectory = func() (string, error) { t.Fatal("invalid input crossed discovery boundary"); return "", nil }
	for _, args := range [][]string{
		{"group"}, {"group", "unknown"}, {"group", "create"}, {"group", "create", ""},
		{"group", "show", "0"}, {"group", "show", "01"}, {"group", "rename", "1"},
		{"group", "edit", "1"}, {"group", "edit", "1", "--revision", "1"},
		{"group", "edit", "1", "--context", "x", "--revision", "0"},
		{"group", "edit", "1", "--context", "x", "--revision", "01"},
		{"group", "edit", "1", "--context", "x", "--revision", "9223372036854775808"},
		{"group", "create", "x", "--context", "one", "--context", "two"},
		{"group", "create", "x", "--context", "one", "--context-file", "-"},
		{"group", "edit", "1", "--clear-context", "--context-file", "-"},
		{"group", "edit", "1", "--clear-context", "--context", ""},
		{"group", "edit", "1", "--clear-context=yes"},
		{"group", "create", "x", "--context-file"}, {"group", "create", "x", "--context-file="},
		{"group", "list", "--context-file", "-"}, {"group", "show", "1", "extra"},
		{"group", "rename", "1", "x", "--context", "y"},
	} {
		out, err, exit := runTestApp(a, args...)
		if exit != 2 || out != "" || !json.Valid([]byte(err)) {
			t.Fatalf("%v: %d %q %q", args, exit, out, err)
		}
	}
	for _, sub := range []string{"create", "list", "show", "edit", "rename"} {
		out, err, exit := runRawApp(a, "group", sub, "--help")
		if exit != 0 || err != "" || !strings.Contains(out, "--context-file -") {
			t.Fatalf("help: %d %q %q", exit, out, err)
		}
	}
}

func TestGroupCLIInvalidPayloadDoesNotCreateOrEdit(t *testing.T) {
	t.Parallel()
	a, root := groupTestApp(t)
	g := runGroup(t, a, "group", "create", "keep", "--context", "original")
	for _, payload := range []string{string([]byte{0xff}), strings.Repeat("x", storage.MaxGroupContextBytes+1)} {
		for _, args := range [][]string{{"group", "create", "invalid"}, {"group", "edit", strconv.FormatInt(g.ID, 10)}} {
			a.stdin = strings.NewReader(payload)
			if err := runPelletError(t, a, 2, append(args, "--context-file", "-")...); !strings.Contains(err, "invalid_group_context") {
				t.Fatal(err)
			}
		}
	}
	a.stdin = errorReader{}
	if err := runPelletError(t, a, 1, "group", "edit", strconv.FormatInt(g.ID, 10), "--context-file", "-"); !strings.Contains(err, "group_context_read_failed") {
		t.Fatal(err)
	}
	runPelletError(t, a, 1, "group", "create", "missing", "--context-file", filepath.Join(root, "missing.md"))
	if got := runGroup(t, a, "group", "show", strconv.FormatInt(g.ID, 10)); got != g {
		t.Fatalf("invalid edit mutated: %+v", got)
	}
	list := runPelletCommand(t, a, "group", "list")
	if strings.Contains(list, "invalid") || strings.Contains(list, "missing") {
		t.Fatal(list)
	}
}

func TestGroupOutputGolden(t *testing.T) {
	g := storage.Group{ID: 7, ProjectID: 2, Name: "parser Ω", Revision: 3, Context: "# Shared context\n\n```mermaid\nflowchart LR\n\tA --> B\n```\n", CreatedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 20, 13, 0, 0, 0, time.UTC)}
	var out bytes.Buffer
	for _, name := range []string{"create", "show", "edit", "rename"} {
		if err := (output.JSONRenderer{}).Render(&out, "group "+name, newGroupData(g)); err != nil {
			t.Fatal(err)
		}
	}
	for _, data := range []any{groupListData{}, groupListData{newGroupSummary(g)}, newGroupData(g), newGroupData(storage.Group{ID: 8, ProjectID: 2, Name: "empty", Revision: 1})} {
		fmt.Fprintln(&out, "human:")
		if err := (output.HumanRenderer{}).Render(&out, "group show", data); err != nil {
			t.Fatal(err)
		}
	}
	if err := (output.JSONRenderer{}).Render(&out, "group list", groupListData{}); err != nil {
		t.Fatal(err)
	}
	if err := (output.JSONRenderer{}).Render(&out, "group list", groupListData{newGroupSummary(g)}); err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "group.golden", out.String())
	var narrow bytes.Buffer
	if err := (output.HumanRenderer{Width: 12}).Render(&narrow, "group show", newGroupData(g)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(narrow.String(), g.Context) {
		t.Fatalf("Markdown layout changed on narrow terminal: %q", narrow.String())
	}
}
