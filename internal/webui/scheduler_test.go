package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/app"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestScheduleHTTPExplicitWorkspaceExactFieldsAndSecurity(t *testing.T) {
	f := newHandlerFixture(t, 2)
	supervisor := app.NewExecutionSupervisor(context.Background(), app.SupervisorOptions{
		Recorder: app.ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
			return sqlite.OpenExecutionRunDatabase(ctx, path)
		}},
		Settings: app.WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
			return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
		}},
	})
	defer supervisor.Close()
	f.application.Scheduler = app.NewScheduler(context.Background(), app.SchedulerOptions{Database: app.Database{Root: filepath.Dir(f.databasePath), Path: f.databasePath}, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}})
	defer f.application.Scheduler.Close()
	path := "/projects/project2/schedules"
	form := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[1].Workspaces[0].ID, 10)}, "mode": {"watch"}, "group": {" Exact "}, "external_id": {"External:ID"}}
	for _, test := range []struct {
		origin string
		cookie bool
	}{{"https://example.invalid", true}, {testOrigin, false}} {
		response := performMutation(f.handler, path, form, test.origin, test.cookie, "application/x-www-form-urlencoded")
		if response.Code != http.StatusForbidden {
			t.Fatalf("security status = %d", response.Code)
		}
	}
	response := performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusAccepted {
		t.Fatalf("start = %d %s", response.Code, response.Body.String())
	}
	var receipt app.ScheduleStatus
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.WorkspaceID != f.projects[1].Workspaces[0].ID || receipt.ProjectID != f.projects[1].ID || *receipt.Group != " Exact " || *receipt.ExternalID != "External:ID" || receipt.Limit != 100 {
		t.Fatalf("receipt: %+v", receipt)
	}
	receiptPath := path + "/" + strconv.FormatInt(receipt.ID, 10)
	response = performRequest(f.handler, http.MethodGet, receiptPath, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status: %d", response.Code)
	}
	response = performRequest(f.handler, http.MethodGet, "/projects/project1/schedules/"+strconv.FormatInt(receipt.ID, 10), "", nil)
	if response.Code == http.StatusOK {
		t.Fatal("cross-project receipt exposed")
	}
	for _, action := range []string{"stop-after", "stop-now"} {
		response = performMutation(f.handler, receiptPath+"/"+action, url.Values{"_csrf": {testCSRF}}, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code != http.StatusAccepted {
			t.Fatalf("stop %s: %d %s", action, response.Code, response.Body.String())
		}
	}
	for _, test := range []struct {
		key    string
		values []string
	}{{"workspace_id", nil}, {"group", []string{""}}, {"group", []string{"one", "two"}}, {"surprise", []string{"value"}}, {"resume_from", []string{"12"}}, {"limit", []string{"0"}}} {
		invalid := url.Values{}
		for key, values := range form {
			invalid[key] = append([]string(nil), values...)
		}
		if test.values == nil {
			delete(invalid, test.key)
		} else {
			invalid[test.key] = test.values
		}
		response = performMutation(f.handler, path, invalid, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code == http.StatusAccepted {
			t.Fatalf("accepted invalid %s=%v", test.key, test.values)
		}
	}
	ungrouped := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[1].Workspaces[0].ID, 10)}, "mode": {"run_one"}, "group_scope": {"ungrouped"}}
	response = performMutation(f.handler, path, ungrouped, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "exact ungrouped") {
		t.Fatalf("ungrouped schedule = %d %s", response.Code, response.Body.String())
	}
	for _, mismatch := range []url.Values{
		{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[1].Workspaces[0].ID, 10)}, "mode": {"run_one"}, "group_scope": {"value"}},
		{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[1].Workspaces[0].ID, 10)}, "mode": {"run_one"}, "group_scope": {"any"}, "group": {"exact"}},
	} {
		response = performMutation(f.handler, path, mismatch, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("accepted mismatched group scope: %d %s", response.Code, response.Body.String())
		}
	}
}

func TestPendingInteractionRendersAfterBrowserReconnectAndDeadProcessRejectsAnswer(t *testing.T) {
	f := newHandlerFixture(t, 1)
	pellet, err := f.application.CreatePellet(context.Background(), f.projects[0], storage.NewPellet{Title: "interactive"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.application.TransitionPellet(context.Background(), f.projects[0], pellet.Reference, storage.PelletVersion(pellet), storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	recorder := app.ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		return sqlite.OpenExecutionRunDatabase(ctx, path)
	}}
	f.application.Executions = app.NewExecutionSupervisor(context.Background(), app.SupervisorOptions{Recorder: recorder})
	defer f.application.Executions.Close()
	f.application.Database = app.Database{Root: filepath.Dir(f.databasePath), Path: f.databasePath}
	capture := storage.RunCapture{ProjectID: f.projects[0].ID, WorkspaceID: f.projects[0].Workspaces[0].ID, PelletNumber: pellet.Reference.Number, Mode: "run_one", StartingHead: strings.Repeat("a", 40),
		Settings:     storage.EffectiveRunSettings{Codex: storage.CodexRunSettings{Executable: "codex", Limits: storage.CodexRunLimits{MaxMessageBytes: 4096, EventBuffer: 8, MaxPending: 8, StderrBytes: 1024}}, ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"},
		PromptPrefix: storage.PromptPrefix{TemplateVersion: "test", SkillSHA256: strings.Repeat("a", 64), HelpSHA256: strings.Repeat("b", 64), ToolExecutable: "pl", ToolVersion: "test", Text: "stable"}}
	capture.ScheduleMode, capture.ScheduleRemaining = "watch", 7
	db, err := sqlite.OpenExecutionRunDatabase(context.Background(), f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateExecutionRun(context.Background(), capture)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	interaction := &storage.RunInteraction{RequestID: `"reconnect"`, Method: "item/tool/requestUserInput", ThreadID: "thread", TurnID: "turn", ItemID: "item", Title: "Codex needs your input", Questions: []storage.InteractionQuestion{{ID: "choice", Header: "Scope", Question: "Which complete choice?", Options: []storage.InteractionOption{{Label: "Focused", Description: "Only the exact target."}}, Other: true}, {ID: "secret", Header: "Secret", Question: "Enter a transient value", Secret: true}}}
	progress := storage.RunProgress{Phase: "implementation", State: "awaiting_input", ThreadID: "thread", TurnID: "turn", Summary: "Codex is awaiting explicit input.", Interaction: interaction}
	run, err = db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces/1", "", nil)
	body := response.Body.String()
	for _, want := range []string{"Which complete choice?", "Focused", "Only the exact target.", `type="password"`, `name="request_id" value="&#34;reconnect&#34;"`} {
		if response.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("reconnected UI missing %q: %d %s", want, response.Code, body)
		}
	}
	form := url.Values{"_csrf": {testCSRF}, "revision": {strconv.FormatInt(run.Revision, 10)}, "request_id": {`"reconnect"`}, "action": {"answer"}, "answer.choice": {"Focused"}, "answer.secret": {"transient"}}
	response = performMutation(f.handler, "/projects/project1/runs/"+strconv.FormatInt(run.ID, 10)+"/interaction", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "process is no longer available") {
		t.Fatalf("dead process answer = %d %s", response.Code, response.Body.String())
	}
	db, err = sqlite.OpenExecutionRunDatabase(context.Background(), f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.InterruptExecutionRun(context.Background(), run.ID, run.Revision, "unknown")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	response = performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces/1", "", nil)
	for _, want := range []string{"Nothing restarts automatically", "at implementation, then continue watch (7 pellets remaining", `name="mode" value="watch"`, `name="resume_from" value="` + strconv.FormatInt(run.ID, 10) + `"`} {
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("saved recovery intent missing %q: %d %s", want, response.Code, response.Body.String())
		}
	}
}
