//go:build darwin || linux

package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestSupervisorPreflightCrashHelper(t *testing.T) {
	encoded := os.Getenv("PELLETS_PREFLIGHT_CRASH_REQUEST")
	if encoded == "" {
		return
	}
	var request ExecutionRequest
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		t.Fatal(err)
	}
	supervisor := NewExecutionSupervisor(context.Background(), SupervisorOptions{
		Recorder: ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
			return sqlite.OpenExecutionRunDatabase(ctx, path)
		}},
		Settings: WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
			return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
		}},
	})
	s := NewScheduler(context.Background(), SchedulerOptions{Database: request.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}})
	handle, err := s.Start(context.Background(), ScheduleRequest{Selected: request.Selected, Mode: "run_one", Limit: 1, Group: request.Capture.Group, ExternalID: request.Capture.ExternalID})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := handle.Result(context.Background()); err != nil || result.State == "needs_attention" {
		t.Fatalf("preflight helper exited: %+v %v", result, err)
	}
}

// Only real custodian settlement authorizes these successful recoveries. The
// child server dies while its owned fake Codex family is gated in account/read,
// before Recorder.Begin, and the paused custodian retains exclusion afterwards.
func crashedPreflight(t *testing.T) (*Scheduler, ScheduleRequest, *sqlite.PelletRepository, []byte) {
	t.Helper()
	executable := installSupervisorPeer(t)
	s, request, q := schedulerFixture(t, executable, "preflight_gate")
	group, external := " Exact Group ", "Exact:ID"
	request.Group, request.ExternalID, request.Limit = &group, &external, 1
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	if _, err := q.UpdatePellet(context.Background(), request.Selected, ref, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &group}, ExternalID: storage.NullableTextChange{Set: true, Value: &external}}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ExecutionRequest{Database: s.options.Database, Selected: request.Selected, Capture: storage.RunCapture{Group: &group, ExternalID: &external}})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestSupervisorPreflightCrashHelper$")
	command.Env = append(os.Environ(), "PELLETS_PREFLIGHT_CRASH_REQUEST="+string(encoded))
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		command.Process.Kill()
		if command.ProcessState == nil {
			_ = command.Wait()
		}
	})
	root := s.options.Database.Root
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "fake-preflight-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned preflight did not reach gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	events := readPeerEvents(t, root)
	guardian := testProcessParent(t, events[0].PID)
	if testProcessParent(t, guardian) != command.Process.Pid {
		t.Fatal("preflight is not owned by the disposable helper")
	}
	if err := syscall.Kill(guardian, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	resumed := false
	t.Cleanup(func() {
		if !resumed {
			syscall.Kill(guardian, syscall.SIGCONT)
		}
	})
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	request.ResumePellet = &ref.Number
	if err := s.options.Supervisor.ReconcileStartup(context.Background(), s.options.Database, recoveryProjects(request.Selected)); err != nil {
		t.Fatal(err)
	}
	if got := awaitSchedule(t, startSchedule(t, s, request)); got.Reason != "workspace_execution_busy" || got.RunID != 0 {
		t.Fatalf("live custodian admitted recovery: %+v", got)
	}
	if err := syscall.Kill(guardian, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	resumed = true
	requireStoppedPeers(t, events)
	gitDir := filepath.Join(root, ".git")
	deadline = time.Now().Add(5 * time.Second)
	for {
		lock, err := executionlock.AcquireRecovery(gitDir)
		if err == nil {
			lock.Close()
			break
		}
		if domain.PublicError(err).Code != "workspace_execution_busy" || time.Now().After(deadline) {
			t.Fatalf("custodian did not settle: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "pellets-execution.lock"))
	if err != nil {
		t.Fatal(err)
	}
	var owner executionlock.Owner
	if err := json.Unmarshal(data, &owner); err != nil || owner.RunID != 0 || owner.Preflight == nil || owner.Preflight.Version != 1 {
		t.Fatalf("missing preflight receipt: %s %v", data, err)
	}
	runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("crash crossed Recorder.Begin: %+v %v", runs, err)
	}
	for _, event := range events {
		if event.Method == "thread/start" || event.Method == "turn/start" {
			t.Fatalf("crash crossed conversation boundary: %+v", event)
		}
	}
	return s, request, q, data
}

func TestPreflightCrashExplicitRecoveryRejectsMismatchesAndStartsOnce(t *testing.T) {
	s, request, _, original := crashedPreflight(t)
	root := s.options.Database.Root
	path := filepath.Join(root, ".git", "pellets-execution.lock")
	if err := s.options.Supervisor.ReconcileStartup(context.Background(), s.options.Database, recoveryProjects(request.Selected)); err != nil {
		t.Fatal(err)
	}
	implicit := request
	implicit.ResumePellet = nil
	if got := awaitSchedule(t, startSchedule(t, s, implicit)); got.Reason != "workspace_execution_recovery_required" {
		t.Fatalf("implicit replay: %+v", got)
	}
	branch := gitForExecutionTest(t, root, "symbolic-ref", "--short", "HEAD")
	gitForExecutionTest(t, root, "switch", "-c", "changed-before-preflight-recovery")
	if got := awaitSchedule(t, startSchedule(t, s, request)); got.Reason != "workspace_execution_recovery_required" {
		t.Fatalf("changed branch admitted: %+v", got)
	}
	gitForExecutionTest(t, root, "switch", branch)
	for _, test := range []struct {
		name   string
		change func(*executionlock.Owner, *ScheduleRequest)
	}{
		{"legacy", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight = nil }},
		{"foreign database", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Database += ".foreign" }},
		{"run receipt", func(o *executionlock.Owner, _ *ScheduleRequest) { o.RunID = 7; o.Preflight = nil }},
		{"project", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.ProjectID++ }},
		{"workspace", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.WorkspaceID++ }},
		{"pellet", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.PelletNumber++ }},
		{"generation", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.ImplementationRevision++ }},
		{"legacy generation", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.ImplementationRevision = 0 }},
		{"platform", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.Platform = "windows" }},
		{"version", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.Version++ }},
		{"database identity", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.DatabaseIdentity += ":changed" }},
		{"git identity", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.GitIdentity += ":changed" }},
		{"head", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.StartingHead = strings.Repeat("a", 40) }},
		{"branch", func(o *executionlock.Owner, _ *ScheduleRequest) { o.Preflight.StartingRef += "-changed" }},
		{"mode", func(_ *executionlock.Owner, r *ScheduleRequest) { r.Mode = "watch" }},
		{"limit", func(_ *executionlock.Owner, r *ScheduleRequest) { r.Limit++ }},
		{"unfiltered", func(_ *executionlock.Owner, r *ScheduleRequest) { r.Group, r.ExternalID = nil, nil }},
		{"receipt token", func(_ *executionlock.Owner, r *ScheduleRequest) { r.PreflightReceipt = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var owner executionlock.Owner
			if err := json.Unmarshal(original, &owner); err != nil {
				t.Fatal(err)
			}
			r := request
			test.change(&owner, &r)
			data, err := json.Marshal(owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if got := awaitSchedule(t, startSchedule(t, s, r)); got.Reason != "workspace_execution_recovery_required" || got.RunID != 0 {
				t.Fatalf("mismatch admitted: %+v", got)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatalf("rejected receipt changed: %s %v", after, err)
			}
		})
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	var saved executionlock.Owner
	if err := json.Unmarshal(original, &saved); err != nil {
		t.Fatal(err)
	}
	request.PreflightReceipt = saved.Preflight.Token()
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("unauthenticated"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := awaitSchedule(t, startSchedule(t, s, request)); got.State != "needs_attention" || got.RunID != 0 {
		t.Fatalf("unrepaired preflight admitted: %+v", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(original) {
		t.Fatalf("failed repair erased receipt: %s %v", after, err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_gate"), 0600); err != nil {
		t.Fatal(err)
	}
	h := startSchedule(t, s, request)
	awaitScheduledTurn(t, s)
	if _, err := s.Start(context.Background(), request); domain.PublicError(err).Code != "workspace_schedule_busy" {
		t.Fatalf("duplicate Resume admitted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-complete"), []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := awaitSchedule(t, h); got.Completed != 1 || got.RunID == 0 {
		t.Fatalf("recovery failed: %+v", got)
	}
	if got := awaitSchedule(t, startSchedule(t, s, request)); got.Completed != 0 || got.RunID != 0 {
		t.Fatalf("repeated Resume duplicated work: %+v", got)
	}
	runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].ResumeFrom != nil || runs[0].State != "completed" {
		t.Fatalf("wrong fresh run: %+v %v", runs, err)
	}
	starts, turns := 0, 0
	for _, event := range readPeerEvents(t, root) {
		switch event.Method {
		case "thread/start":
			starts++
		case "turn/start":
			turns++
		case "thread/resume":
			t.Fatal("no-run recovery resumed an existing conversation")
		}
	}
	if starts != 1 || turns != 1 {
		t.Fatalf("duplicate conversation: starts=%d turns=%d", starts, turns)
	}
}

func TestPreflightCrashRecoveryRevalidatesGenerationAfterRepair(t *testing.T) {
	s, request, q, original := crashedPreflight(t)
	root := s.options.Database.Root
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	entered, proceed := make(chan struct{}), make(chan struct{})
	prepare := s.options.Supervisor.options.Prepare
	s.options.Supervisor.options.Prepare = func(ctx context.Context, options codex.PrepareOptions) (*codex.PreparedRun, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-proceed:
		}
		return prepare(ctx, options)
	}
	h := startSchedule(t, s, request)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("recovery did not enter preflight")
	}
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: *request.ResumePellet}
	for _, operation := range []storage.PelletLifecycleOperation{storage.PelletRelease, storage.PelletStart} {
		if _, err := q.TransitionPellet(context.Background(), request.Selected, ref, storage.PelletLifecycleRequest{Operation: operation}); err != nil {
			t.Fatal(err)
		}
	}
	close(proceed)
	if got := awaitSchedule(t, h); got.State != "needs_attention" || got.RunID != 0 || got.Completed != 0 {
		t.Fatalf("changed generation executed: %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(root, ".git", "pellets-execution.lock"))
	if err != nil || string(data) != string(original) {
		t.Fatalf("generation conflict changed receipt: %s %v", data, err)
	}
	if got := awaitSchedule(t, startSchedule(t, s, request)); got.Reason != "workspace_execution_recovery_required" {
		t.Fatalf("stale generation replay: %+v", got)
	}
	runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("generation race created runs: %+v %v", runs, err)
	}
}
