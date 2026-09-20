package webui

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"strings"
	"testing"

	"pellets/internal/storage"
)

func TestRunDetailsDistinguishHistoricalGroupContextAndEscapeSource(t *testing.T) {
	templates, err := template.ParseFS(embeddedFiles, "templates/run_context.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"captured", "empty", "ungrouped", "legacy"} {
		t.Run(state, func(t *testing.T) {
			snapshot := storage.GroupContextSnapshot{Version: 1, State: state}
			doc := "# Original\n```mermaid\nflowchart LR\n A --> B\n```\n<script>window.contextInjected=true</script>"
			want := ""
			switch state {
			case "captured", "empty":
				if state == "empty" {
					doc = ""
					want = "The captured group document was empty."
				} else {
					want = html.EscapeString(doc)
				}
				snapshot.State, snapshot.Group = "captured", &storage.GroupContext{ID: 42, Name: "Original <group>", Revision: 7, Context: doc}
			case "ungrouped":
				want = "Ungrouped at admission."
			case "legacy":
				want = "Legacy execution: no group context was captured."
			}
			filter := "different schedule filter"
			view := makeRunView(storage.ExecutionRun{ID: 19, GroupContext: snapshot, RunCapture: storage.RunCapture{Group: &filter}})
			var rendered bytes.Buffer
			if err := templates.ExecuteTemplate(&rendered, "run-group-context", view); err != nil {
				t.Fatal(err)
			}
			body := rendered.String()
			if !strings.Contains(body, want) || !strings.Contains(body, "Historical snapshot") || strings.Contains(body, filter) || strings.Contains(body, "<script>") || strings.Contains(body, "<pl-diagram") {
				t.Fatalf("historical source rendered incorrectly: %s", body)
			}
			if snapshot.Group != nil && (!strings.Contains(body, "Original &lt;group&gt;") || !strings.Contains(body, "42 / 7")) {
				t.Fatal("captured identity missing")
			}
			if state == "captured" && !strings.Contains(body, `tabindex="0" aria-label="Captured Markdown source"`) {
				t.Fatal("source cannot be focused for scrolling")
			}
		})
	}
}

func TestReviewDetailsPairTargetsWithHistoricalSourceWithoutDuplication(t *testing.T) {
	templates, err := template.ParseFS(embeddedFiles, "templates/run_context.html")
	if err != nil {
		t.Fatal(err)
	}
	doc := "# Original\r\n<script>never execute</script>"
	contexts := []storage.GroupContextSnapshot{
		{Version: 1, State: "captured", Group: &storage.GroupContext{ID: 42, Name: "Original <group>", Revision: 7, Context: doc}},
		{Version: 1, State: "captured", Group: &storage.GroupContext{ID: 42, Name: "Original <group>", Revision: 8, Context: "Second revision"}},
		{Version: 1, State: "captured", Group: &storage.GroupContext{ID: 43, Name: "Empty", Revision: 1}},
		{Version: 1, State: "ungrouped"}, {Version: 1, State: "legacy"},
	}
	contexts = append(contexts, contexts[0])
	run := storage.ExecutionRun{ID: 19}
	run.Mode = "review_checkpoint"
	run.ReviewSnapshot = &storage.ReviewSnapshot{Version: 2}
	for i, context := range contexts {
		reference := fmt.Sprintf("project-%d", i+1)
		run.ReviewSnapshot.Targets = append(run.ReviewSnapshot.Targets, storage.ReviewTarget{Reference: reference})
		run.ReviewSnapshot.GroupContexts = append(run.ReviewSnapshot.GroupContexts, storage.ReviewGroupContext{RunID: int64(i + 10), Snapshot: context})
	}
	view := makeRunView(run)
	var rendered bytes.Buffer
	if err := templates.ExecuteTemplate(&rendered, "review-group-context", view); err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	if len(view.ReviewContexts) != 5 || strings.Count(body, html.EscapeString(doc)) != 1 || strings.Contains(body, "<script>") {
		t.Fatalf("duplicated or unsafe source: %s", body)
	}
	for _, want := range []string{"project-1", "project-6", "Implementation run 10", "Implementation run 15", "42 / 7", "42 / 8", "Second revision", "The captured group document was empty", "Ungrouped at admission", "Legacy execution", `id="run-group-source-review-19-0"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, version := range []int{0, 1} {
		run.ReviewSnapshot = &storage.ReviewSnapshot{Version: version}
		rendered.Reset()
		if err := templates.ExecuteTemplate(&rendered, "review-group-context", makeRunView(run)); err != nil {
			t.Fatal(err)
		}
		want := "Legacy review: implementation group context was not captured"
		if version == 0 {
			want = "Implementation context has not been captured for review yet"
		}
		if !strings.Contains(rendered.String(), want) {
			t.Fatal("legacy and uncaptured review conflated")
		}
	}
}
