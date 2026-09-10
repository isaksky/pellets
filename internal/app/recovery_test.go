package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func recoveryProjects(selected storage.ResolvedProject) []storage.WebProjectSummary {
	project := selected.Project
	project.Workspaces = []storage.Workspace{selected.Workspace}
	return []storage.WebProjectSummary{{Project: project}}
}

func savedRecoveryRun(t *testing.T, supervisor *ExecutionSupervisor, request ExecutionRequest, phase string) storage.ExecutionRun {
	t.Helper()
	run, err := supervisor.options.Recorder.Begin(context.Background(), request.Database, request.Selected, request.Capture)
	if err != nil {
		t.Fatal(err)
	}
	progress := run.RunProgress
	progress.Phase, progress.ThreadID, progress.TurnID = phase, "thread", "turn"
	run, err = supervisor.options.Recorder.Save(context.Background(), request.Database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRecoveryStartupPreservesPhaseAndNeverRuns(t *testing.T) {
	for _, phase := range []string{"implementation", "review", "finalization"} {
		t.Run(phase, func(t *testing.T) {
			supervisor, request := supervisorFixture(t, "must-not-execute")
			request.Capture.Mode, request.Capture.ScheduleMode, request.Capture.ScheduleRemaining = "review_checkpoint", "watch", 7
			run := savedRecoveryRun(t, supervisor, request, phase)
			var prepared atomic.Int64
			supervisor.options.Prepare = func(context.Context, codex.PrepareOptions) (*codex.PreparedRun, error) {
				prepared.Add(1)
				return nil, errors.New("startup must not prepare")
			}
			for i := 0; i < 2; i++ {
				if err := supervisor.ReconcileStartup(context.Background(), request.Database, recoveryProjects(request.Selected)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := supervisor.ReadRun(context.Background(), request.Database, run.ID)
			if err != nil || got.State != "interrupted" || got.Phase != phase || got.ThreadID != run.ThreadID || got.TurnID != run.TurnID || got.ScheduleMode != "watch" || got.ScheduleRemaining != 7 || got.Revision != run.Revision+1 || prepared.Load() != 0 {
				t.Fatalf("startup = %+v, %v, preparations=%d", got, err, prepared.Load())
			}
		})
	}
}

func TestRecoveryStartupLeavesLiveLockOwnerAlone(t *testing.T) {
	supervisor, request := supervisorFixture(t, "must-not-execute")
	run := savedRecoveryRun(t, supervisor, request, "implementation")
	lock, err := executionlock.Acquire(filepath.Join(request.Database.Root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := supervisor.ReconcileStartup(context.Background(), request.Database, recoveryProjects(request.Selected)); err != nil {
		t.Fatal(err)
	}
	got, err := supervisor.ReadRun(context.Background(), request.Database, run.ID)
	if err != nil || got.Revision != run.Revision || got.State != "running" {
		t.Fatalf("live run changed: %+v %v", got, err)
	}
}

func TestRecoveryMissingWorktreePreservesEvidence(t *testing.T) {
	supervisor, request := supervisorFixture(t, "must-not-execute")
	run := savedRecoveryRun(t, supervisor, request, "implementation")
	gitDir := filepath.Join(request.Database.Root, ".git")
	if err := os.Rename(gitDir, gitDir+".unavailable"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(gitDir+".unavailable", gitDir)
	if err := supervisor.ReconcileStartup(context.Background(), request.Database, recoveryProjects(request.Selected)); err != nil {
		t.Fatal(err)
	}
	got, err := supervisor.ReadRun(context.Background(), request.Database, run.ID)
	if err != nil || got.StartingHead != run.StartingHead || got.State != "needs_attention" || got.ErrorCode != "workspace_unavailable" || got.ThreadID != run.ThreadID {
		t.Fatalf("evidence lost: %+v %v", got, err)
	}
}

func TestRecoveryResumeValidatesBeforeStartingCodex(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct{ name, code string }{{"branch", "resume_branch_changed"}, {"head", "implementation_head_changed"}, {"lifecycle", "schedule_resume_changed"}, {"missing_history", "resume_history_unavailable"}, {"active_history", "resume_history_not_stopped"}, {"checkpoint", "checkpoint_resume_policy_required"}} {
		t.Run(test.name, func(t *testing.T) {
			supervisor, execution := supervisorFixture(t, executable)
			if err := os.WriteFile(filepath.Join(execution.Database.Root, "fake-events.jsonl"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if test.name == "checkpoint" {
				execution.Capture.Mode = "review_checkpoint"
			}
			run := savedRecoveryRun(t, supervisor, execution, "implementation")
			if err := supervisor.ReconcileStartup(context.Background(), execution.Database, recoveryProjects(execution.Selected)); err != nil {
				t.Fatal(err)
			}
			mode := "schedule_success"
			if strings.HasSuffix(test.name, "history") {
				mode = "schedule_" + test.name
			}
			if err := os.WriteFile(filepath.Join(execution.Database.Root, "fake-mode"), []byte(mode), 0600); err != nil {
				t.Fatal(err)
			}
			if test.name == "branch" {
				gitForExecutionTest(t, execution.Database.Root, "switch", "-c", "changed-at-same-head")
			}
			if test.name == "head" {
				gitForExecutionTest(t, execution.Database.Root, "commit", "--allow-empty", "-m", "external work")
			}
			if test.name == "lifecycle" {
				q, err := sqlite.OpenPelletRepository(context.Background(), execution.Database.Path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = q.TransitionPellet(context.Background(), execution.Selected, domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber}, storage.PelletLifecycleRequest{Operation: storage.PelletRelease})
				q.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			s := NewScheduler(context.Background(), SchedulerOptions{Database: execution.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
				return sqlite.OpenPelletRepository(ctx, path)
			}})
			defer s.Close()
			status := awaitSchedule(t, startSchedule(t, s, ScheduleRequest{Selected: execution.Selected, Mode: "run_one", ResumePellet: &run.PelletNumber, ResumeFrom: &run.ID}))
			if status.State != "needs_attention" || status.Reason != test.code {
				t.Fatalf("resume = %+v", status)
			}
			for _, event := range readPeerEvents(t, execution.Database.Root) {
				if event.Method == "turn/start" || event.Method == "thread/resume" || event.Method == "thread/start" {
					t.Fatalf("invalid recovery generated work: %s", event.Method)
				}
			}
			if s.WorkspaceAttention(run.WorkspaceID) == "" {
				t.Fatal("recovery error disappeared from UI")
			}
			if test.name == "missing_history" {
				data, err := os.ReadFile(filepath.Join(execution.Database.Root, ".git", "pellets-execution.lock"))
				if err != nil || len(data) != 0 {
					t.Fatalf("read-only probe manufactured a crash fence: %q %v", data, err)
				}
				if err := os.WriteFile(filepath.Join(execution.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
					t.Fatal(err)
				}
				recovered := awaitSchedule(t, startSchedule(t, s, ScheduleRequest{Selected: execution.Selected, Mode: "run_one", ResumePellet: &run.PelletNumber, ResumeFrom: &run.ID}))
				if recovered.Completed != 1 {
					t.Fatalf("restored history could not Resume: %+v", recovered)
				}
			}
		})
	}
}

type recoveryFaultDatabase struct {
	storage.ExecutionRunDatabase
	create *atomic.Bool
	finish *atomic.Bool
}

func TestRecoverySettledConversationRejectsNewerHistory(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, boundary := range []string{"settled_turn_start", "settled_thread_start", "failed_turn_start"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			supervisor, request := supervisorFixture(t, executable)
			run, err := supervisor.options.Recorder.Begin(ctx, request.Database, request.Selected, request.Capture)
			if err != nil {
				t.Fatal(err)
			}
			db, err := supervisor.options.Recorder.repository(ctx, request.Database)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			run, err = db.BeginExecutionOperation(ctx, run.ID, run.Revision, "thread/start")
			if err != nil {
				t.Fatal(err)
			}
			run, err = db.FinishExecutionOperation(ctx, storage.ExecutionOperationResult{ID: run.ID, PendingRevision: run.PendingRevision, ThreadID: "thread"})
			if err != nil {
				t.Fatal(err)
			}
			if boundary != "settled_thread_start" {
				run, err = db.BeginExecutionOperation(ctx, run.ID, run.Revision, "turn/start")
				if err != nil {
					t.Fatal(err)
				}
				result := storage.ExecutionOperationResult{ID: run.ID, PendingRevision: run.PendingRevision, ThreadID: "thread", TurnID: "turn"}
				if boundary == "failed_turn_start" {
					result.ThreadID, result.TurnID, result.ErrorCode = "", "", "codex_call_unconfirmed"
				}
				run, err = db.FinishExecutionOperation(ctx, result)
				if err != nil {
					t.Fatal(err)
				}
			}
			if run.PendingOperation != "" || (run.TurnID != "") != (boundary == "settled_turn_start") {
				t.Fatalf("wrong durable boundary: %+v", run)
			}
			if err := supervisor.ReconcileStartup(ctx, request.Database, recoveryProjects(request.Selected)); err != nil {
				t.Fatal(err)
			}
			before, err := supervisor.ReadRun(ctx, request.Database, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(request.Database.Root, "fake-mode"), []byte("schedule_advanced_history"), 0600); err != nil {
				t.Fatal(err)
			}
			s := NewScheduler(ctx, SchedulerOptions{Database: request.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
				return sqlite.OpenPelletRepository(ctx, path)
			}})
			defer s.Close()
			status := awaitSchedule(t, startSchedule(t, s, ScheduleRequest{Selected: request.Selected, Mode: "run_one", ResumePellet: &run.PelletNumber, ResumeFrom: &run.ID}))
			if status.State != "needs_attention" || status.Reason != "resume_history_advanced" {
				t.Fatalf("external history was accepted: %+v", status)
			}
			for _, event := range readPeerEvents(t, request.Database.Root) {
				if event.Method == "thread/start" || event.Method == "thread/resume" || event.Method == "turn/start" {
					t.Fatalf("new generation reached %s", event.Method)
				}
			}
			runs, err := supervisor.ListWorkspaceRuns(ctx, request.Database, run.WorkspaceID, 5)
			if err != nil || len(runs) != 1 || runs[0].Revision != before.Revision || runs[0].TurnID != before.TurnID || runs[0].Phase != before.Phase {
				t.Fatalf("rejection changed saved evidence: %+v %v", runs, err)
			}
		})
	}
}

func TestRecoveryCompletedSaveBeforeReceiptCleanupNeverRecommits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uncertain crashed Job Objects remain fenced on Windows")
	}
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_success")
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.Completed != 1 {
		t.Fatalf("first run: %+v", first)
	}
	previous, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
	ordinary := awaitSchedule(t, startSchedule(t, s, request))
	if ordinary.Reason != "execution_run_conflict" || ordinary.Started != 0 {
		t.Fatalf("ordinary completion could Resume: %+v", ordinary)
	}
	// Recreate the exact durable boundary after a successful terminal save but
	// before receipt cleanup. No running process or queue state is fabricated.
	lock, err := executionlock.Acquire(filepath.Join(s.options.Database.Root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Record(executionlock.Owner{Database: s.options.Database.Path, RunID: previous.ID}); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	if !s.options.Supervisor.RecoveryPending(s.options.Database, previous) {
		t.Fatal("completed crash receipt unavailable to UI")
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_failed"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered := awaitSchedule(t, startSchedule(t, s, request))
	if recovered.Completed != 1 || recovered.State != "completed" {
		t.Fatalf("completed receipt stranded: %+v", recovered)
	}
	if count := gitForExecutionTest(t, s.options.Database.Root, "rev-list", "--count", previous.StartingHead+"..HEAD"); count != "1" {
		t.Fatalf("duplicate commit: %s", count)
	}
	turns := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			turns++
		}
	}
	if turns != 1 || s.options.Supervisor.RecoveryPending(s.options.Database, previous) {
		t.Fatalf("recovery repeated generation or retained fence: turns=%d", turns)
	}
}

func (db recoveryFaultDatabase) CreateExecutionRun(ctx context.Context, capture storage.RunCapture) (storage.ExecutionRun, error) {
	run, err := db.ExecutionRunDatabase.CreateExecutionRun(ctx, capture)
	if err == nil && db.create != nil && db.create.CompareAndSwap(false, true) {
		return storage.ExecutionRun{}, errors.New("injected crash after durable creation")
	}
	return run, err
}

func (db recoveryFaultDatabase) FinishExecutionOperation(ctx context.Context, result storage.ExecutionOperationResult) (storage.ExecutionRun, error) {
	if result.TurnID != "" && db.finish != nil && db.finish.CompareAndSwap(false, true) {
		return storage.ExecutionRun{}, errors.New("injected crash before turn identity save")
	}
	return db.ExecutionRunDatabase.FinishExecutionOperation(ctx, result)
}

func TestRecoveryExplicitResumeReconcilesDurableWriteAndCallFaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unnamed job cleanup uncertainty deliberately retains the receipt on Windows")
	}
	executable := installSupervisorPeer(t)
	for _, fault := range []string{"create", "turn_result"} {
		t.Run(fault, func(t *testing.T) {
			s, request, _ := schedulerFixture(t, executable, "schedule_gate")
			var create, finish atomic.Bool
			open := s.options.Supervisor.options.Recorder.Open
			s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
				db, err := open(ctx, path)
				if err != nil {
					return nil, err
				}
				wrapper := recoveryFaultDatabase{ExecutionRunDatabase: db}
				if fault == "create" {
					wrapper.create = &create
				} else {
					wrapper.finish = &finish
				}
				return wrapper, nil
			}
			first := awaitSchedule(t, startSchedule(t, s, request))
			if first.State != "needs_attention" {
				t.Fatalf("fault was lost: %+v", first)
			}
			runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 5)
			if err != nil || len(runs) != 1 {
				t.Fatalf("durable attempts: %+v %v", runs, err)
			}
			previous := runs[0]
			if err := s.options.Supervisor.ReconcileStartup(context.Background(), s.options.Database, recoveryProjects(request.Selected)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
				t.Fatal(err)
			}
			request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
			second := awaitSchedule(t, startSchedule(t, s, request))
			if second.State != "completed" || second.Completed != 1 {
				t.Fatalf("explicit recovery: %+v", second)
			}
			if count := gitForExecutionTest(t, s.options.Database.Root, "rev-list", "--count", previous.StartingHead+"..HEAD"); count != "1" {
				t.Fatalf("commits = %s", count)
			}
			old, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, previous.ID)
			if err != nil || old.PendingOperation != "" || storage.RunActive(old.State) {
				t.Fatalf("old call not reconciled: %+v %v", old, err)
			}
		})
	}
}

func TestRecoveryUnfinishedTurnAndChildExitPauseSavedMode(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"schedule_unfinished", "schedule_exit"} {
		t.Run(mode, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, mode)
			request.Mode, request.Limit = "watch", 2
			second, err := q.CreatePellet(context.Background(), request.Selected, storage.NewPellet{Title: "second"})
			if err != nil {
				t.Fatal(err)
			}
			first := awaitSchedule(t, startSchedule(t, s, request))
			if first.State != "needs_attention" || first.Started != 1 || first.Completed != 0 {
				t.Fatalf("unfinished schedule advanced: %+v", first)
			}
			runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 5)
			if err != nil || len(runs) != 1 {
				t.Fatalf("unexpected retry: %+v %v", runs, err)
			}
			previous := runs[0]
			if previous.ScheduleMode != "watch" || previous.ScheduleRemaining != 2 {
				t.Fatalf("saved mode lost: %+v", previous.RunCapture)
			}
			untouched, err := q.ReadPellet(context.Background(), request.Selected, second.Reference)
			if err != nil || untouched.Status != domain.PelletOpen {
				t.Fatalf("next pellet changed: %+v %v", untouched, err)
			}
			if mode == "schedule_unfinished" && !strings.Contains(previous.Summary, "unfinished work") {
				t.Fatalf("pause was not explained: %+v", previous)
			}
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
				t.Fatal(err)
			}
			request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
			request.Mode, request.Limit = "run_one", 999 // Browser controls cannot replace saved intent.
			resumed := awaitSchedule(t, startSchedule(t, s, request))
			if resumed.Mode != "watch" || resumed.Limit != 2 || resumed.Completed != 2 || resumed.Reason != "limit_reached" {
				t.Fatalf("saved mode not resumed: %+v", resumed)
			}
			latest, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, resumed.RunID)
			if err != nil || latest.ScheduleRemaining != 1 {
				t.Fatalf("remaining budget not decremented: %+v %v", latest, err)
			}
		})
	}
}

func TestRecoveryCheckpointUsesOnlyExplicitPhaseReconciler(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	request.Capture.Mode = "review_checkpoint"
	previous := savedRecoveryRun(t, supervisor, request, "review")
	if err := supervisor.ReconcileStartup(context.Background(), request.Database, recoveryProjects(request.Selected)); err != nil {
		t.Fatal(err)
	}
	var drives, resumes atomic.Int64
	s := NewScheduler(context.Background(), SchedulerOptions{Database: request.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}, Checkpoints: &CheckpointExecutionPolicy{
		Matches: func(storage.Pellet) bool { return true },
		Drive: func(context.Context, *WorkspaceExecution) error {
			drives.Add(1)
			return errors.New("must not repeat checkpoint work")
		},
		Resume: func(ctx context.Context, execution *WorkspaceExecution) error {
			resumes.Add(1)
			run, err := execution.Read(ctx)
			if err != nil {
				return err
			}
			if run.Phase != "review" || run.ResumeFrom == nil || *run.ResumeFrom != previous.ID || run.TurnID != previous.TurnID {
				return errors.New("checkpoint evidence lost")
			}
			return scheduleError("checkpoint_reconciled_for_test", "the exact saved review receipt was reconciled without another follow-up")
		},
	}})
	defer s.Close()
	status := awaitSchedule(t, startSchedule(t, s, ScheduleRequest{Selected: request.Selected, Mode: "run_one", ResumeFrom: &previous.ID, ResumePellet: &previous.PelletNumber}))
	if drives.Load() != 0 || resumes.Load() != 1 || status.Reason != "checkpoint_reconciled_for_test" {
		t.Fatalf("checkpoint restarted: %+v drive=%d resume=%d", status, drives.Load(), resumes.Load())
	}
	for _, event := range readPeerEvents(t, request.Database.Root) {
		if event.Method == "turn/start" || event.Method == "turn/interrupt" {
			t.Fatal("checkpoint recovery acted on a historical generation")
		}
	}
}
