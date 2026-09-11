package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
	"pellets/internal/testcodex"
)

func TestMain(m *testing.M) {
	if testcodex.Run() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Only the external Codex protocol peer is deterministic. Git, the database,
// request handler, scheduler, supervisor, preflight and finalization are real.
func recoveryHandlerFixture(t *testing.T) (handlerFixture, string) {
	t.Helper()
	f := newHandlerFixture(t, 1)
	root := filepath.Join(filepath.Dir(f.databasePath), "project1")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"config", "commit.gpgSign", "false"}, {"commit", "--allow-empty", "-m", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("/fake-*\n/.agents/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(root, ".agents", "skills", "pellets")
	if err := os.MkdirAll(skill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: pellets\n---\nDisposable integration skill.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	contents, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	toolName := "pl"
	if runtime.GOOS == "windows" {
		toolName += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, toolName), contents, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PELLETS_SUPERVISOR_PEER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	f.application.Database = app.Database{Root: filepath.Dir(f.databasePath), Path: f.databasePath}
	settings := app.WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
		return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
	}}
	if _, err := settings.Save(context.Background(), f.application.Database, storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: f.projects[0].Workspaces[0].ID, Settings: storage.CodexRunSettings{Executable: executable}}); err != nil {
		t.Fatal(err)
	}
	f.application.Executions = app.NewExecutionSupervisor(context.Background(), app.SupervisorOptions{Settings: settings, Recorder: app.ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		return sqlite.OpenExecutionRunDatabase(ctx, path)
	}}})
	t.Cleanup(func() { f.application.Executions.Close() })
	f.application.Scheduler = app.NewScheduler(context.Background(), app.SchedulerOptions{Database: f.application.Database, Supervisor: f.application.Executions, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}, Checkpoints: app.NewCheckpointReviewPolicy(f.application.Database, func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	})})
	t.Cleanup(func() { f.application.Scheduler.Close() })
	return f, root
}

func awaitHTTPSchedule(t *testing.T, f handlerFixture, form url.Values) app.ScheduleStatus {
	t.Helper()
	h := startHTTPSchedule(t, f, form)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := h.Result(ctx)
	if err != nil {
		t.Fatalf("schedule: %+v %v", h.Status(), err)
	}
	return status
}

func startHTTPSchedule(t *testing.T, f handlerFixture, form url.Values) *app.ScheduleHandle {
	t.Helper()
	response := performMutation(f.handler, "/projects/project1/schedules", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusAccepted {
		t.Fatalf("schedule POST = %d %s", response.Code, response.Body.String())
	}
	var receipt app.ScheduleStatus
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	h, err := f.application.Scheduler.Get(receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHTTPReopenedGenerationResumesWithFreshConversationAfterPreflightRepair(t *testing.T) {
	f, root := recoveryHandlerFixture(t)
	ctx := context.Background()
	pellet, err := f.application.CreatePellet(ctx, f.projects[0], storage.NewPellet{Title: "reopen exact generation"})
	if err != nil {
		t.Fatal(err)
	}
	modeFile := filepath.Join(root, "fake-mode")
	setMode := func(mode string) {
		t.Helper()
		if err := os.WriteFile(modeFile, []byte(mode), 0600); err != nil {
			t.Fatal(err)
		}
	}
	form := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[0].Workspaces[0].ID, 10)}, "mode": {"run_one"}}
	setMode("schedule_success")
	first := awaitHTTPSchedule(t, f, form)
	if first.Completed != 1 {
		t.Fatalf("first generation: %+v", first)
	}
	for _, operation := range []storage.PelletLifecycleOperation{storage.PelletReopen, storage.PelletStart} {
		current, err := f.application.Pellet(ctx, f.projects[0], pellet.Reference)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.application.TransitionPellet(ctx, f.projects[0], pellet.Reference, storage.PelletVersion(current), storage.PelletLifecycleRequest{Operation: operation}); err != nil {
			t.Fatal(err)
		}
	}
	form.Set("resume_pellet", strconv.FormatInt(pellet.Reference.Number, 10))
	setMode("unauthenticated")
	failed := awaitHTTPSchedule(t, f, form)
	if failed.State != "needs_attention" || failed.RunID != 0 || failed.Reason == "schedule_exact_resume_required" {
		t.Fatalf("reopened preflight: %+v", failed)
	}
	runs, err := f.application.WorkspaceRuns(ctx, first.WorkspaceID)
	if err != nil || len(runs) != 1 || runs[0].ID != first.RunID {
		t.Fatalf("preflight captured a new run: %+v %v", runs, err)
	}
	oldRevision := runs[0].ImplementationRevision
	response := performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces/1", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "data-no-run-resume") || strings.Contains(response.Body.String(), `name="resume_from"`) {
		t.Fatalf("old generation blocked fresh Resume: %s", response.Body.String())
	}
	setMode("schedule_reimplementation")
	resumed := awaitHTTPSchedule(t, f, form)
	if resumed.Completed != 1 || resumed.RunID == first.RunID {
		t.Fatalf("fresh generation Resume: %+v", resumed)
	}
	runs, err = f.application.WorkspaceRuns(ctx, first.WorkspaceID)
	if err != nil || len(runs) != 2 || runs[0].ResumeFrom != nil || runs[0].ImplementationRevision <= oldRevision || runs[1].ID != first.RunID {
		t.Fatalf("generation/conversation evidence: %+v %v", runs, err)
	}
	events, err := os.ReadFile(filepath.Join(root, "fake-events.jsonl"))
	if err != nil || strings.Count(string(events), `"method":"thread/start"`) != 2 || strings.Contains(string(events), `"method":"thread/resume"`) {
		t.Fatalf("new generation reused old conversation: %v", err)
	}
}

func TestHTTPCurrentGenerationRunRequiresExactAttemptAndPreventsOverlap(t *testing.T) {
	f, root := recoveryHandlerFixture(t)
	ctx := context.Background()
	pellet, err := f.application.CreatePellet(ctx, f.projects[0], storage.NewPellet{Title: "current generation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_input_live"), 0600); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[0].Workspaces[0].ID, 10)}, "mode": {"run_one"}}
	h := startHTTPSchedule(t, f, form)
	var run storage.ExecutionRun
	deadline := time.Now().Add(15 * time.Second)
	for {
		runs, err := f.application.WorkspaceRuns(ctx, f.projects[0].Workspaces[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 1 && runs[0].Interaction != nil {
			run = runs[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("current run did not start: %+v", h.Status())
		}
		time.Sleep(10 * time.Millisecond)
	}
	form.Set("resume_pellet", strconv.FormatInt(pellet.Reference.Number, 10))
	response := performMutation(f.handler, "/projects/project1/schedules", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusConflict {
		t.Fatalf("overlap accepted: %d", response.Code)
	}
	h.StopNow()
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := h.Result(wait); err != nil {
		t.Fatal(err)
	}
	if got := awaitHTTPSchedule(t, f, form); got.Reason != "schedule_exact_resume_required" || got.RunID != 0 {
		t.Fatalf("current conversation bypassed: %+v", got)
	}
	response = performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces/1", "", nil)
	if strings.Contains(response.Body.String(), "data-no-run-resume") || !strings.Contains(response.Body.String(), `name="resume_from" value="`+strconv.FormatInt(run.ID, 10)+`"`) {
		t.Fatalf("current generation recovery lost its exact attempt: %s", response.Body.String())
	}
	form.Set("resume_from", strconv.FormatInt(run.ID, 10))
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := awaitHTTPSchedule(t, f, form); got.Completed != 1 {
		t.Fatalf("exact current attempt failed: %+v", got)
	}
}

func TestHTTPResumeWithoutRunRepairsPreflightAndReconstructsExactIntent(t *testing.T) {
	for _, mode := range []string{"run_one", "drain", "watch"} {
		t.Run(mode, func(t *testing.T) {
			f, root := recoveryHandlerFixture(t)
			group, external := " Exact Group ", "Exact:ID"
			pellet, err := f.application.CreatePellet(context.Background(), f.projects[0], storage.NewPellet{Title: "recover preflight", Group: &group, ExternalID: &external})
			if err != nil {
				t.Fatal(err)
			}
			other, err := f.application.CreatePellet(context.Background(), f.projects[0], storage.NewPellet{Title: "leave queued", Group: &group, ExternalID: &external})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("unauthenticated"), 0600); err != nil {
				t.Fatal(err)
			}
			form := url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(f.projects[0].Workspaces[0].ID, 10)}, "mode": {mode}, "group": {group}, "external_id": {external}, "limit": {"1"}}
			failed := awaitHTTPSchedule(t, f, form)
			if failed.State != "needs_attention" || failed.RunID != 0 || failed.PelletNumber != pellet.Reference.Number {
				t.Fatalf("failed preflight: %+v", failed)
			}
			runs, err := f.application.WorkspaceRuns(context.Background(), failed.WorkspaceID)
			if err != nil || len(runs) != 0 {
				t.Fatalf("preflight created run: %+v %v", runs, err)
			}
			response := performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces/1?group=n&external_id=different", "", nil)
			for _, want := range []string{"data-no-run-resume", "No run or saved schedule intent exists", `name="mode" required`, `name="resume_pellet" value="1"`, `name="group" value=" Exact Group "`, `name="external_id" value="Exact:ID"`} {
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
					t.Fatalf("missing recovery %q: %s", want, response.Body.String())
				}
			}
			if strings.Contains(response.Body.String(), `name="resume_from"`) {
				t.Fatal("no-run recovery invented a saved conversation")
			}
			form.Set("resume_pellet", strconv.FormatInt(pellet.Reference.Number, 10))
			for _, origin := range []string{"https://example.invalid", testOrigin} {
				response := performMutation(f.handler, "/projects/project1/schedules", form, origin, origin != testOrigin, "application/x-www-form-urlencoded")
				if response.Code != http.StatusForbidden {
					t.Fatalf("untrusted Resume accepted: %d", response.Code)
				}
			}
			form.Set("group", "different")
			if got := awaitHTTPSchedule(t, f, form); got.Reason != "schedule_filter_mismatch" || got.RunID != 0 {
				t.Fatalf("wrong filter admitted: %+v", got)
			}
			form.Set("group", group)
			form.Set("resume_pellet", strconv.FormatInt(other.Reference.Number, 10))
			if got := awaitHTTPSchedule(t, f, form); got.Reason != "schedule_resume_required" || got.RunID != 0 {
				t.Fatalf("different pellet admitted: %+v", got)
			}
			form.Set("resume_pellet", strconv.FormatInt(pellet.Reference.Number, 10))
			if mode == "run_one" {
				// A stale browser form cannot select a different open pellet when
				// its owner has released the exact target in the meantime.
				current, err := f.application.Pellet(context.Background(), f.projects[0], pellet.Reference)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.application.TransitionPellet(context.Background(), f.projects[0], pellet.Reference, storage.PelletVersion(current), storage.PelletLifecycleRequest{Operation: storage.PelletRelease}); err != nil {
					t.Fatal(err)
				}
				if got := awaitHTTPSchedule(t, f, form); got.Reason != "schedule_resume_changed" || got.RunID != 0 {
					t.Fatalf("stale ownership admitted: %+v", got)
				}
				current, err = f.application.Pellet(context.Background(), f.projects[0], pellet.Reference)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.application.TransitionPellet(context.Background(), f.projects[0], pellet.Reference, storage.PelletVersion(current), storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
					t.Fatal(err)
				}
				gitDir, moved := filepath.Join(root, ".git"), filepath.Join(root, "fake-git-paused")
				if err := os.Rename(gitDir, moved); err != nil {
					t.Fatal(err)
				}
				got := awaitHTTPSchedule(t, f, form)
				if err := os.Rename(moved, gitDir); err != nil {
					t.Fatal(err)
				}
				if got.State != "needs_attention" || got.RunID != 0 {
					t.Fatalf("changed repository identity admitted: %+v", got)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
				t.Fatal(err)
			}
			result := awaitHTTPSchedule(t, f, form)
			if result.Completed != 1 || result.RunID == 0 || result.Mode != mode || result.Limit != 1 {
				t.Fatalf("repaired Resume: %+v", result)
			}
			runs, err = f.application.WorkspaceRuns(context.Background(), failed.WorkspaceID)
			if err != nil || len(runs) != 1 || runs[0].ResumeFrom != nil || runs[0].PelletNumber != pellet.Reference.Number || runs[0].State != "completed" || runs[0].ScheduleMode != mode || *runs[0].Group != group || *runs[0].ExternalID != external {
				t.Fatalf("wrong durable attempt: %+v %v", runs, err)
			}
			open := domain.PelletOpen
			remaining, err := f.application.Pellets(context.Background(), f.projects[0], storage.WebPelletFilters{Status: &open})
			if err != nil || len(remaining) != 1 || remaining[0].Reference != other.Reference {
				t.Fatalf("different target executed: %+v %v", remaining, err)
			}
		})
	}
}
