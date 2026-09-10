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
