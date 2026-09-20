package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/output"
	"pellets/internal/storage"
)

func assertPelletContext(t *testing.T, raw string, want *storage.GroupContext) pelletDetailData {
	t.Helper()
	var envelope struct {
		Command string                     `json:"command"`
		Data    map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatal(err)
	}
	fields := envelope.Data
	if envelope.Command == "next" || envelope.Command == "start-next" {
		if err := json.Unmarshal(fields["pellet"], &fields); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := fields["group_context"]; !ok {
		t.Fatalf("missing group_context: %s", raw)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var pellet pelletDetailData
	if err := json.Unmarshal(encoded, &pellet); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pellet.GroupContext, want) {
		t.Fatalf("context = %+v, want %+v", pellet.GroupContext, want)
	}
	if want == nil && pellet.Group != nil || want != nil && (pellet.Group == nil || *pellet.Group != want.Name) {
		t.Fatalf("group name and context disagree: %+v", pellet)
	}
	return pellet
}

func TestPelletContextCommandsAndOutputModes(t *testing.T) {
	t.Parallel()
	markdown := "# Shared Ω\n\n```mermaid\nflowchart LR\n\tA --> B\n```\n\n**Raw** <b>text</b> $literal `shell`\n"
	for _, kind := range []string{"ungrouped", "empty", "document"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := groupTestApp(t)
			a.stdin = errorReader{}
			for _, command := range []string{"next", "start-next"} {
				raw := runPelletCommand(t, a, command)
				if !strings.Contains(raw, `"selection_reason":"none","pellet":null`) || strings.Contains(raw, "group_context") {
					t.Fatalf("empty selection: %s", raw)
				}
			}
			var want *storage.GroupContext
			args := []string{"add", "member", "--description", "Specific pellet description"}
			if kind != "ungrouped" {
				context := ""
				if kind == "document" {
					context = markdown
				}
				g := runGroup(t, a, "group", "create", "Shared", "--context", context)
				want = &storage.GroupContext{ID: g.ID, Name: g.Name, Revision: g.Revision, Context: g.Context}
				args = append(args, "--group", g.Name)
			}
			runPelletCommand(t, a, args...)
			for _, command := range [][]string{{"show", "groups-1"}, {"next"}, {"start", "groups-1"}, {"start-next"}} {
				for _, terminal := range []bool{false, true} {
					a.isInteractive = func(io.Reader, io.Writer) bool { return terminal }
					for _, format := range [][]string{nil, {"--human"}, {"--json"}, {"--pretty"}} {
						argv := append(append([]string{}, format...), command...)
						out, diagnostics, code := runRawApp(a, argv...)
						if code != 0 || diagnostics != "" {
							t.Fatalf("%v: %d %q %q", argv, code, out, diagnostics)
						}
						machine := len(format) > 0 && (format[0] == "--json" || format[0] == "--pretty")
						if machine {
							assertPelletContext(t, out, want)
						} else {
							for _, text := range []string{"Specific pellet description", "Group context:\n"} {
								if !strings.Contains(out, text) {
									t.Fatalf("%v missing %q: %q", argv, text, out)
								}
							}
							text := "(ungrouped)"
							if want != nil {
								text = "(empty)"
								if want.Context != "" {
									text = want.Context
								}
							}
							if json.Valid([]byte(out)) || !strings.Contains(out, text) {
								t.Fatalf("human output: %q", out)
							}
						}
					}
				}
			}
			// Also exercise a fresh atomic claim, not only resume.
			runPelletCommand(t, a, "release", "groups-1")
			assertPelletContext(t, runPelletCommand(t, a, "start-next"), want)
		})
	}
}

func TestPelletContextTracksCurrentMembershipAndOwnership(t *testing.T) {
	t.Parallel()
	a, _ := groupTestApp(t)
	g := runGroup(t, a, "group", "create", "Exact", "--context", "original")
	runPelletCommand(t, a, "add", "member", "--group", g.Name, "--external-id", "Case:Exact")
	for _, command := range []string{"next", "start-next"} {
		for _, filters := range [][]string{{"--group", "exact"}, {"--external-id", "case:exact"}, {"--group", "missing"}} {
			if raw := runPelletCommand(t, a, append([]string{command}, filters...)...); !strings.Contains(raw, `"pellet":null`) {
				t.Fatalf("inexact filter selected work: %s", raw)
			}
		}
	}
	want := &storage.GroupContext{ID: g.ID, Name: g.Name, Revision: g.Revision, Context: g.Context}
	assertPelletContext(t, runPelletCommand(t, a, "next", "--group", "Exact", "--external-id", "Case:Exact"), want)
	assertPelletContext(t, runPelletCommand(t, a, "start-next", "--group", "Exact", "--external-id", "Case:Exact"), want)
	id := strconv.FormatInt(g.ID, 10)
	checkOwned := func() {
		t.Helper()
		for _, args := range [][]string{{"show", "groups-1"}, {"start", "groups-1"}, {"next", "--group", "missing", "--external-id", "missing"}, {"start-next", "--group", "missing", "--external-id", "missing"}} {
			raw := runPelletCommand(t, a, args...)
			assertPelletContext(t, raw, want)
			if args[0] == "next" || args[0] == "start-next" {
				if !strings.Contains(raw, `"selection_reason":"resume_in_progress"`) {
					t.Fatal(raw)
				}
			}
		}
	}
	g = runGroup(t, a, "group", "edit", id, "--context", "changed\r\n\tΩ\x00")
	want.Context, want.Revision = g.Context, g.Revision
	checkOwned()
	g = runGroup(t, a, "group", "rename", id, "Renamed")
	want.Name, want.Revision = g.Name, g.Revision
	checkOwned()
	g = runGroup(t, a, "group", "create", "Other", "--context", "other document")
	runPelletCommand(t, a, "edit", "groups-1", "--group", g.Name)
	want = &storage.GroupContext{ID: g.ID, Name: g.Name, Revision: g.Revision, Context: g.Context}
	checkOwned()
	runPelletCommand(t, a, "edit", "groups-1", "--clear-group")
	want = nil
	checkOwned()
}

func TestPelletCompactCommandsOmitGroupDocuments(t *testing.T) {
	t.Parallel()
	a, _ := groupTestApp(t)
	g := runGroup(t, a, "group", "create", "shared", "--context", "PRIVATE-DOCUMENT-BODY")
	for _, args := range [][]string{
		{"add", "member", "--group", g.Name}, {"add", "second", "--group", g.Name},
		{"list"}, {"search", "member"}, {"edit", "groups-1", "--description", "updated"},
		{"move", "groups-1", "--after", "groups-2"}, {"defer", "groups-1"},
		{"reopen", "groups-1"}, {"release", "groups-1"}, {"close", "groups-1"},
	} {
		if args[0] == "release" {
			runPelletCommand(t, a, "start", "groups-1")
		}
		raw := runPelletCommand(t, a, args...)
		if strings.Contains(raw, "group_context") || strings.Contains(raw, g.Context) || !strings.Contains(raw, `"group":"shared"`) {
			t.Fatalf("compact %v: %s", args, raw)
		}
	}
}

func TestPelletContextProjectIsolationAndFullMarkdown(t *testing.T) {
	t.Parallel()
	common := t.TempDir()
	runPelletCommand(t, initDBTestApp(common), "init-db")
	current := common
	a := projectTestApp(&current)
	var groups []groupData
	// The maximum document must survive JSON without truncation, even with
	// newlines, tabs, CRLF, HTML and non-ASCII text.
	markdown := strings.Repeat("Ω\t\r\n<b>\n", storage.MaxGroupContextBytes/len("Ω\t\r\n<b>\n"))
	markdown += strings.Repeat("x", storage.MaxGroupContextBytes-len(markdown))
	for _, project := range []string{"alpha", "beta"} {
		current = filepath.Join(common, project)
		if err := os.Mkdir(current, 0755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, current, "init", "-q")
		g := runGroup(t, a, "group", "create", "same", "--context", markdown)
		groups = append(groups, g)
		runPelletCommand(t, a, "add", "member", "--group", "same")
		for _, args := range [][]string{{"show", project + "-1"}, {"next"}, {"start", project + "-1"}, {"start-next"}} {
			assertPelletContext(t, runPelletCommand(t, a, args...), &storage.GroupContext{ID: g.ID, Name: g.Name, Revision: g.Revision, Context: markdown})
		}
		markdown = "foreign document"
	}
	if groups[0].ID == groups[1].ID {
		t.Fatal("projects share group identity")
	}
	runPelletError(t, a, 2, "show", "alpha-1")
}

func TestPelletContextHumanGolden(t *testing.T) {
	var out bytes.Buffer
	for _, group := range []*storage.GroupContext{nil, {ID: 7, Name: "empty", Revision: 1}, {ID: 8, Name: "parser", Revision: 3, Context: "# Shared\n\n```mermaid\nflowchart LR\n\tA --> B\n```"}} {
		data := pelletDetailData{pelletData: pelletData{
			ID: "foo-1", Project: "foo", Number: 1, Title: "Implement parser",
			Description: "Keep identifiers intact.", Status: "open",
			CreatedAt: "2026-09-20T12:00:00Z", UpdatedAt: "2026-09-20T12:00:00Z",
		}, GroupContext: group}
		if group != nil {
			data.Group = &group.Name
		}
		for _, command := range []string{"next", "start-next", "start", "show"} {
			fmt.Fprintln(&out, command+":")
			var result any = data
			if command == "next" || command == "start-next" {
				result = nextData{Pellet: &data}
			}
			if err := (output.HumanRenderer{Width: 12}).Render(&out, command, result); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertGolden(t, "pellet-context-human.golden", out.String())
}
