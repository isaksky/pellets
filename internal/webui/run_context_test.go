package webui

import (
	"bytes"
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
