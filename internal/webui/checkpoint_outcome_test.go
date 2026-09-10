package webui

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestCheckpointInspectorRetainsPurgedReferenceWithoutBrokenLink(t *testing.T) {
	templates, err := template.ParseFS(embeddedFiles, "templates/checkpoint.html")
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	outcome := storage.CheckpointOutcome{ProjectID: 1, CheckpointNumber: 2, ImplementationRevision: 1, Status: "findings", ReviewCompleted: true, Findings: 2, Assessed: 2, Triage: "complete", Dispositions: []storage.CheckpointDisposition{
		{FindingNumber: 1, Decision: "valid", PelletNumber: 3, PelletPresent: false},
		{FindingNumber: 2, Decision: "existing", PelletNumber: 4, PelletPresent: true},
	}}
	view := makeCheckpointOutcomeView(outcome, "renamed", url.Values{"q": {"scope"}}, storage.WebPelletSort{})
	if err := templates.ExecuteTemplate(&rendered, "checkpoint-outcome", view); err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	if !strings.Contains(body, "<code>renamed-3</code> (no longer present)") || strings.Contains(body, "/tasks/renamed-3") || !strings.Contains(body, "/tasks/renamed-4?") || !strings.Contains(body, "&amp;q=scope") || !strings.Contains(body, "Covered by existing Pellet") {
		t.Fatalf("reference rendering: %s", body)
	}
}

// Exercise the real scheduler, review/triage writers, and HTTP reads. Only the
// external model protocol is deterministic; no outcome or execution is stubbed.
func TestHTTPCheckpointDurableOutcomes(t *testing.T) {
	for _, mode := range []string{"review_clean", "review_findings", "review_findings_partial", "review_findings_invalid", "review_missing_result"} {
		t.Run(mode, func(t *testing.T) {
			f, root := recoveryHandlerFixture(t)
			ctx, project := context.Background(), f.projects[0]
			setMode := func(mode string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte(mode), 0600); err != nil {
					t.Fatal(err)
				}
			}
			target, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "selected implementation"})
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(project.Workspaces[0].ID, 10)}, "mode": {"run_one"}}
			setMode("schedule_success")
			if status := awaitHTTPSchedule(t, f, form); status.Completed != 1 {
				t.Fatalf("implementation: %+v", status)
			}
			cp, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "Review selected implementation", Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{target.Reference}})
			if err != nil {
				t.Fatal(err)
			}
			setMode(mode)
			status := awaitHTTPSchedule(t, f, form)
			path := "/projects/project1/tasks/" + cp.Reference.String()
			read := func(h http.Handler, live bool) string {
				t.Helper()
				headers := http.Header{}
				if live {
					headers = http.Header{"Datastar-Request": {"true"}, "Pellets-Target": {"live"}}
				}
				response := performRequest(h, http.MethodGet, path, "", headers)
				if response.Code != http.StatusOK {
					t.Fatalf("inspector: %d %s", response.Code, response.Body.String())
				}
				body := response.Body.String()
				start := strings.Index(body, `<div data-checkpoint-outcome`)
				if start < 0 {
					t.Fatalf("no checkpoint outcome: %s", body)
				}
				end := strings.Index(body[start:], `</div>`)
				if end < 0 {
					t.Fatal("no outcome end")
				}
				return body[start : start+end]
			}
			assertContains := func(body string, want ...string) {
				t.Helper()
				for _, s := range want {
					if !strings.Contains(body, s) {
						t.Fatalf("missing %q: %s", s, body)
					}
				}
			}
			body := read(f.handler, false)
			if strings.Contains(body, "Pending") || strings.Contains(body, "none recorded") {
				t.Fatalf("durable result replaced by placeholder: %s", body)
			}
			switch mode {
			case "review_missing_result":
				if status.State != "needs_attention" {
					t.Fatalf("failed review: %+v", status)
				}
				assertContains(body, "Needs attention")
				return
			case "review_clean":
				assertContains(body, "Clean", "Complete", "0 of 0", "0 created", "No findings")
			case "review_findings_invalid":
				assertContains(body, "Findings", "Complete", "1 of 1", "Invalid finding", "0 created")
			case "review_findings":
				assertContains(body, "Findings", "Complete", "1 of 1", "project1-3", `@navigate('inspector-host')`)
			case "review_findings_partial":
				if status.State != "needs_attention" {
					t.Fatalf("partial: %+v", status)
				}
				assertContains(body, "Findings", "Needs attention", "Partial", "1 of 2", "project1-3")
			}
			// New read-only pool and handler model a reconnect/server restart,
			// including one with no execution supervisor or workspace history.
			reader, err := sqlite.OpenWebReader(ctx, f.databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			reconnected, err := newHandler(&app.WebApplication{Reader: reader}, newEventHub(), handlerConfig{Host: testHost, Origin: testOrigin, CSRF: testCSRF})
			if err != nil {
				t.Fatal(err)
			}
			if fresh := read(reconnected, false); fresh != body {
				t.Fatalf("reconnect changed outcome:\n%s\n%s", body, fresh)
			}
			if mode == "review_findings_partial" {
				setMode("review_findings")
				form.Set("resume_from", strconv.FormatInt(status.RunID, 10))
				form.Set("resume_pellet", strconv.FormatInt(cp.Reference.Number, 10))
				if recovered := awaitHTTPSchedule(t, f, form); recovered.Completed != 1 {
					t.Fatalf("resume: %+v", recovered)
				}
				body = read(reconnected, false)
				assertContains(body, "Complete", "2 of 2", "project1-3", "project1-4")
				if strings.Contains(body, "Partial") || strings.Contains(body, "Needs attention") {
					t.Fatalf("stale partial result: %s", body)
				}
				form.Del("resume_from")
				form.Del("resume_pellet")
			}
			closed, err := f.application.Pellet(ctx, project, cp.Reference)
			if err != nil || closed.Status != domain.PelletClosed {
				t.Fatalf("checkpoint not closed: %+v %v", closed, err)
			}
			if mode == "review_clean" {
				conflict := performMutation(f.handler, "/projects/project1/pellets/"+cp.Reference.String()+"/edit", url.Values{
					"_csrf": {testCSRF}, "version": {storage.PelletVersion(cp)}, "title": {cp.Title}, "description": {cp.Description}, "external_id": {""}, "group": {""},
				}, testOrigin, true, "application/x-www-form-urlencoded")
				if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "Review outcome:</strong> Clean") {
					t.Fatalf("conflict inspector lost durable completion: %d %s", conflict.Code, conflict.Body.String())
				}
			}
			setMode("schedule_success")
			if _, err = f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "later ordinary work"}); err != nil {
				t.Fatal(err)
			}
			if later := awaitHTTPSchedule(t, f, form); later.Completed != 1 {
				t.Fatalf("later run: %+v", later)
			}
			live := read(f.handler, true)
			assertContains(live, "Review outcome:", "Complete")
			if fresh := read(reconnected, false); fresh != body {
				t.Fatalf("later workspace run replaced closed receipt:\n%s\n%s", body, fresh)
			}
			// Reopening this exact number must not inherit its prior generation.
			if _, err = f.application.TransitionPellet(ctx, project, cp.Reference, storage.PelletVersion(closed), storage.PelletLifecycleRequest{Operation: storage.PelletReopen}); err != nil {
				t.Fatal(err)
			}
			fresh := read(reconnected, false)
			assertContains(fresh, "Pending separate review", `data-checkpoint-generation="2"`)
			if strings.Contains(fresh, "distinct findings assessed") || strings.Contains(fresh, "project1-3") {
				t.Fatalf("new generation inherited review: %s", fresh)
			}
		})
	}
}
