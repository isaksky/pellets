//go:build darwin || linux

package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"pellets/internal/app"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
	"pellets/internal/testcodex"
)

func TestHTTPPreflightCrashHelper(t *testing.T) {
	encoded := os.Getenv("PELLETS_HTTP_PREFLIGHT_CRASH_REQUEST")
	if encoded == "" {
		return
	}
	var request app.ExecutionRequest
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		t.Fatal(err)
	}
	supervisor := app.NewExecutionSupervisor(context.Background(), app.SupervisorOptions{
		Recorder: app.ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
			return sqlite.OpenExecutionRunDatabase(ctx, path)
		}},
		Settings: app.WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
			return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
		}},
	})
	scheduler := app.NewScheduler(context.Background(), app.SchedulerOptions{Database: request.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}})
	handle, err := scheduler.Start(context.Background(), app.ScheduleRequest{Selected: request.Selected, Mode: "watch", Limit: 1, Group: request.Capture.Group, ExternalID: request.Capture.ExternalID, ResumePellet: request.ResumePellet})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := handle.Result(context.Background()); err != nil || result.State == "needs_attention" {
		t.Fatalf("preflight helper exited: %+v %v", result, err)
	}
}

func TestHTTPPreflightCrashReceiptPreservesMultilineFilters(t *testing.T) {
	f, root := recoveryHandlerFixture(t)
	ctx := context.Background()
	group, external := " Group\r\nline\n<saved>\rend ", "External\rID\n<saved>\r\nend"
	pellet, err := f.application.CreatePellet(ctx, f.projects[0], storage.NewPellet{Title: "multiline recovery", Group: &group, ExternalID: &external})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.application.TransitionPellet(ctx, f.projects[0], pellet.Reference, storage.PelletVersion(pellet), storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("preflight_gate"), 0600); err != nil {
		t.Fatal(err)
	}
	selected := storage.ResolvedProject{Project: f.projects[0], Workspace: f.projects[0].Workspaces[0]}
	encoded, err := json.Marshal(app.ExecutionRequest{Database: f.application.Database, Selected: selected, Capture: storage.RunCapture{Group: &group, ExternalID: &external}, ResumePellet: &pellet.Reference.Number})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "-test.run=^TestHTTPPreflightCrashHelper$")
	child.Env = append(os.Environ(), "PELLETS_HTTP_PREFLIGHT_CRASH_REQUEST="+string(encoded))
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		child.Process.Kill()
		if child.ProcessState == nil {
			_ = child.Wait()
		}
	})
	wait := func(message string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ready() {
			if time.Now().After(deadline) {
				t.Fatal(message)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("preflight did not reach owned process gate", func() bool { _, err := os.Stat(filepath.Join(root, "fake-preflight-ready")); return err == nil })
	events, err := os.ReadFile(filepath.Join(root, "fake-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(events)), "\n") {
		var event struct {
			PID    int    `json:"pid"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Method == "process" {
			pids = append(pids, event.PID)
		}
	}
	if len(pids) != 3 {
		t.Fatalf("preflight must own root, child, grandchild: %v", pids)
	}
	parent := func(pid int) int {
		data, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatal(err)
		}
		value, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	guardian := parent(pids[0])
	if parent(guardian) != child.Process.Pid {
		t.Fatal("preflight custodian is not owned by the disposable helper")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	pids = append(pids, guardian)
	wait("custodian did not settle the real process family", func() bool {
		for _, pid := range pids {
			if testcodex.Alive(pid) {
				return false
			}
		}
		return true
	})
	runs, err := f.application.WorkspaceRuns(ctx, selected.Workspace.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("preflight crash created a run: %+v %v", runs, err)
	}
	response := performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces/1", "", nil)
	formHTML := regexp.MustCompile(`(?s)<form[^>]*data-no-run-resume.*?</form>`).FindString(response.Body.String())
	token := regexp.MustCompile(`name="preflight_receipt" value="([0-9a-f]{64})"`).FindStringSubmatch(formHTML)
	if response.Code != http.StatusOK || len(token) != 2 || strings.Contains(formHTML, `name="group"`) || strings.Contains(formHTML, `name="external_id"`) || !strings.Contains(formHTML, "&lt;saved&gt;") {
		t.Fatalf("missing safe receipt form: %s", formHTML)
	}
	form := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(selected.Workspace.ID, 10)}, "resume_pellet": {strconv.FormatInt(pellet.Reference.Number, 10)}, "mode": {"watch"}, "limit": {"1"}, "preflight_receipt": {token[1]}}
	lockPath := filepath.Join(root, ".git", "pellets-execution.lock")
	before, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		field, value string
		status       int
	}{
		{"preflight_receipt", strings.Repeat("0", 64), http.StatusConflict},
		{"group", group, http.StatusUnprocessableEntity},
		{"external_id", external, http.StatusUnprocessableEntity},
		{"group_scope", "any", http.StatusUnprocessableEntity},
	} {
		changed := url.Values{}
		for key, values := range form {
			changed[key] = append([]string(nil), values...)
		}
		changed.Set(test.field, test.value)
		response := performMutation(f.handler, "/projects/project1/schedules", changed, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code != test.status {
			t.Fatalf("%s tamper status %d: %s", test.field, response.Code, response.Body.String())
		}
	}
	after, err := os.ReadFile(lockPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("tampering changed receipt: %s %v", after, err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	result := awaitHTTPSchedule(t, f, form)
	if result.Completed != 1 || result.RunID == 0 || result.Group == nil || *result.Group != group || result.ExternalID == nil || *result.ExternalID != external {
		t.Fatalf("lost exact schedule bytes: %+v", result)
	}
	runs, err = f.application.WorkspaceRuns(ctx, selected.Workspace.ID)
	if err != nil || len(runs) != 1 || runs[0].ResumeFrom != nil || runs[0].Group == nil || *runs[0].Group != group || runs[0].ExternalID == nil || *runs[0].ExternalID != external {
		t.Fatalf("lost exact captured bytes: %+v %v", runs, err)
	}
}
