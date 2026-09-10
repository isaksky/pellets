package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func preparedReviewCheckpoint(t *testing.T, executable, reviewMode string) (*Scheduler, ScheduleRequest, storage.Pellet, []storage.ExecutionRun) {
	t.Helper()
	s, request, queue := schedulerFixture(t, executable, "schedule_success")
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "AGENTS.md"), []byte("Review changes for correctness and exact scope.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForExecutionTest(t, s.options.Database.Root, "add", "AGENTS.md")
	gitForExecutionTest(t, s.options.Database.Root, "commit", "-m", "test instructions")
	for i := 0; i < 2; i++ {
		if _, err := queue.CreatePellet(context.Background(), request.Selected, storage.NewPellet{Title: "target"}); err != nil {
			t.Fatal(err)
		}
	}
	var runs []storage.ExecutionRun
	for i := 0; i < 3; i++ {
		status := awaitSchedule(t, startSchedule(t, s, request))
		if status.State != "completed" {
			t.Fatalf("implementation %d: %+v", i, status)
		}
		run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}
	checkpoint, err := queue.CreatePellet(context.Background(), request.Selected, storage.NewPellet{
		Title: "Review noncontiguous targets", Kind: domain.PelletReviewCheckpoint,
		ReviewTargets: []domain.PelletReference{{ProjectCode: request.Selected.Project.Code, Number: 1}, {ProjectCode: request.Selected.Project.Code, Number: 3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte(reviewMode), 0600); err != nil {
		t.Fatal(err)
	}
	s.options.Checkpoints = NewCheckpointReviewPolicy(s.options.Database, s.options.OpenQueue)
	return s, request, checkpoint, runs
}

func TestCheckpointReviewerUsesFreshDetachedContextAndExactCommitSet(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct {
		mode, status string
		findings     int
	}{{"review_clean", "clean", 0}, {"review_findings", "findings", 1}} {
		t.Run(test.mode, func(t *testing.T) {
			s, request, checkpoint, implementations := preparedReviewCheckpoint(t, executable, test.mode)
			handle := startSchedule(t, s, request)
			status := awaitSchedule(t, handle)
			if status.State != "completed" || status.Completed != 1 {
				t.Fatalf("review status: %+v error=%+v", status, s.options.Supervisor.err)
			}
			run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if run.ReviewResult == nil || run.ReviewResult.Status != test.status || len(run.ReviewResult.Findings) != test.findings || run.ReviewSnapshot == nil || len(run.ReviewSnapshot.Commits) != 2 || len(run.ReviewSnapshot.Instructions) != 2 {
				t.Fatalf("review evidence: %#v %#v", run.ReviewSnapshot, run.ReviewResult)
			}
			if test.findings == 1 {
				finding := run.ReviewResult.Findings[0]
				if finding.Title != "Handle edge case" || finding.Body != "The selected change misses the empty input case." || finding.Priority != 1 || finding.File != "demo-1.txt" || finding.Line != 1 {
					t.Fatalf("native finding normalization: %#v", finding)
				}
				if run.CheckpointTriage == nil || len(run.CheckpointTriage.Assessments) != 1 || run.CheckpointTriage.Assessments[0].Decision != "valid" || run.CheckpointTriage.Assessments[0].PelletNumber < 1 || run.CheckpointTriage.Assessments[0].ThreadID == run.ThreadID {
					t.Fatalf("separate triage receipt missing: %+v", run.CheckpointTriage)
				}
			}
			closed, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
			if err != nil {
				t.Fatal(err)
			}
			pellet, readErr := closed.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
			err = errorsJoin(readErr, closed.Close())
			if err != nil || pellet.Status != domain.PelletClosed {
				t.Fatalf("checkpoint not closed: %+v %v", pellet, err)
			}

			var seed, review map[string]any
			for _, event := range readPeerEvents(t, s.options.Database.Root) {
				if event.Method == "thread/start" {
					_ = json.Unmarshal(event.Params, &seed)
				}
				if event.Method == "review/start" {
					_ = json.Unmarshal(event.Params, &review)
				}
				if event.Method == "turn/start" {
					var params map[string]any
					_ = json.Unmarshal(event.Params, &params)
					if strings.HasPrefix(fmt.Sprint(params["threadId"]), "triage-thread-") {
						if params["sandboxPolicy"].(map[string]any)["type"] != "readOnly" || params["outputSchema"] == nil {
							t.Fatalf("triage turn not read-only/structured: %+v", params)
						}
						input := params["input"].([]any)[0].(map[string]any)["text"].(string)
						assertCapturedCheckpointPrefix(t, input, run.PromptPrefix.Text, "Independently triage exactly")
						for _, required := range []string{"applicable AGENTS.md", "active_queue", "prior_assessments", "verifiable acceptance criteria"} {
							if !strings.Contains(input, required) {
								t.Fatalf("triage prompt omits %q", required)
							}
						}
					}
				}
			}
			if seed["sandbox"] != "read-only" || seed["approvalsReviewer"] != "auto_review" {
				t.Fatalf("seed policy: %#v", seed)
			}
			if review["delivery"] != "detached" || review["threadId"] != "review-seed" {
				t.Fatalf("review delivery: %#v", review)
			}
			target := review["target"].(map[string]any)
			instructions := target["instructions"].(string)
			assertCapturedCheckpointPrefix(t, instructions, run.PromptPrefix.Text, "Review exactly the immutable checkpoint")
			encoded, err := json.Marshal(run.ReviewSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(instructions, "Checkpoint "+checkpoint.Reference.String()+":\n"+string(encoded)) || !strings.Contains(instructions, "`"+reviewCleanMarker(encoded)+"`") || strings.Contains(string(encoded), "PELLETS CODEX TASK PRELOAD") {
				t.Fatal("prefix changed the immutable snapshot or its scope-bound clean marker")
			}
			first, third := implementations[0].ResultCommit, implementations[2].ResultCommit
			if target["type"] != "custom" || !strings.Contains(instructions, first) || !strings.Contains(instructions, third) || strings.Contains(instructions, first+".."+third) {
				t.Fatalf("inexact custom scope: %s", instructions)
			}
		})
	}
}

func assertCapturedCheckpointPrefix(t *testing.T, prompt, prefix, role string) {
	t.Helper()
	if prefix == "" || !strings.HasPrefix(prompt, prefix+role) || strings.Count(prompt, prefix) != 1 {
		t.Fatal("fresh checkpoint conversation must start with exactly one captured prefix followed by its role restrictions")
	}
}

func TestCheckpointReviewCompletedReceiptReconcilesCrashBeforeLockCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uncertain crashed Job Objects remain fenced on Windows")
	}
	executable := installSupervisorPeer(t)
	s, request, checkpoint, _ := preparedReviewCheckpoint(t, executable, "review_clean")
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "completed" || first.Completed != 1 {
		t.Fatalf("initial review: %+v", first)
	}
	receipt, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, first.RunID)
	if err != nil || !storage.CompletedReviewReceipt(receipt) {
		t.Fatalf("completed review receipt: %+v %v", receipt, err)
	}
	queue, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	closed, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
	err = errorsJoin(readErr, queue.Close())
	if err != nil || closed.Status != domain.PelletClosed {
		t.Fatalf("closed checkpoint: %+v %v", closed, err)
	}
	closedVersion := storage.PelletVersion(closed)

	// Recreate the exact crash boundary after the atomic completion/close but
	// before the supervisor cleaned its persistent recovery receipt.
	lock, err := executionlock.Acquire(filepath.Join(s.options.Database.Root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Record(executionlock.Owner{Database: s.options.Database.Path, RunID: receipt.ID}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &receipt.ID, &receipt.PelletNumber
	recovered := awaitSchedule(t, startSchedule(t, s, request))
	if recovered.State != "completed" || recovered.Completed != 1 || recovered.RunID == receipt.ID {
		failed, _ := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, recovered.RunID)
		t.Fatalf("completed review receipt was not reconciled: %+v run=%+v", recovered, failed)
	}
	resumed, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, recovered.RunID)
	if err != nil || !storage.CompletedReviewReceipt(resumed) || resumed.ResumeFrom == nil || *resumed.ResumeFrom != receipt.ID {
		t.Fatalf("reconciled review attempt: %+v %v", resumed, err)
	}
	reviews := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "review/start" {
			reviews++
		}
	}
	queue, err = s.options.OpenQueue(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	after, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
	err = errorsJoin(readErr, queue.Close())
	if err != nil || storage.PelletVersion(after) != closedVersion || reviews != 1 || s.options.Supervisor.RecoveryPending(s.options.Database, resumed) {
		t.Fatalf("reconciliation repeated side effects: version=%q want=%q reviews=%d pending=%t err=%v", storage.PelletVersion(after), closedVersion, reviews, s.options.Supervisor.RecoveryPending(s.options.Database, resumed), err)
	}
}

type reviewCompletionGateDatabase struct {
	storage.ExecutionRunDatabase
	entered chan struct{}
	once    *sync.Once
}

func (db reviewCompletionGateDatabase) CompleteReviewCheckpoint(ctx context.Context, id, revision int64) (storage.ExecutionRun, error) {
	db.once.Do(func() { close(db.entered) })
	<-ctx.Done()
	return storage.ExecutionRun{}, ctx.Err()
}

func TestCheckpointReviewInterruptedReconciliationLineageCanResumeAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uncertain crashed Job Objects remain fenced on Windows")
	}
	executable := installSupervisorPeer(t)
	s, request, checkpoint, _ := preparedReviewCheckpoint(t, executable, "review_clean")
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "completed" {
		t.Fatalf("initial review: %+v", first)
	}
	previous, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, first.RunID)
	if err != nil || !storage.CompletedReviewReceipt(previous) {
		t.Fatalf("initial receipt: %+v %v", previous, err)
	}
	queue, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	closed, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
	err = errorsJoin(readErr, queue.Close())
	if err != nil {
		t.Fatal(err)
	}
	closedVersion := storage.PelletVersion(closed)
	baseOpen := s.options.Supervisor.options.Recorder.Open

	for attempt := 0; attempt < 2; attempt++ {
		lock, err := executionlock.Acquire(filepath.Join(s.options.Database.Root, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.Record(executionlock.Owner{Database: s.options.Database.Path, RunID: previous.ID}); err != nil {
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}

		entered := make(chan struct{})
		once := &sync.Once{}
		s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
			db, err := baseOpen(ctx, path)
			if err != nil {
				return nil, err
			}
			return reviewCompletionGateDatabase{ExecutionRunDatabase: db, entered: entered, once: once}, nil
		}
		request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
		handle := startSchedule(t, s, request)
		select {
		case <-entered:
		case <-time.After(15 * time.Second):
			t.Fatal("reconciliation did not reach atomic completion boundary")
		}
		handle.StopNow()
		status := awaitSchedule(t, handle)
		s.options.Supervisor.options.Recorder.Open = baseOpen
		previous, err = s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
		if err != nil || previous.State != "interrupted" || !storage.ReviewReconciliationAttempt(previous) {
			t.Fatalf("interrupted reconciliation %d: %+v %v", attempt+1, previous, err)
		}
	}

	lock, err := executionlock.Acquire(filepath.Join(s.options.Database.Root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Record(executionlock.Owner{Database: s.options.Database.Path, RunID: previous.ID}); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
	final := awaitSchedule(t, startSchedule(t, s, request))
	if final.State != "completed" || final.Completed != 1 {
		t.Fatalf("final lineage reconciliation: %+v", final)
	}
	completed, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, final.RunID)
	if err != nil || !storage.CompletedReviewReceipt(completed) || completed.Attempt != previous.Attempt+1 {
		t.Fatalf("completed lineage descendant: %+v %v", completed, err)
	}
	reviews := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "review/start" {
			reviews++
		}
	}
	queue, err = s.options.OpenQueue(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	after, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
	err = errorsJoin(readErr, queue.Close())
	if err != nil || reviews != 1 || storage.PelletVersion(after) != closedVersion || s.options.Supervisor.RecoveryPending(s.options.Database, completed) {
		t.Fatalf("lineage replayed side effects: reviews=%d version=%q want=%q pending=%t err=%v", reviews, storage.PelletVersion(after), closedVersion, s.options.Supervisor.RecoveryPending(s.options.Database, completed), err)
	}
}

func TestCheckpointReviewerUsesExactCommitsFromDifferentWorktrees(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, queue := schedulerFixture(t, executable, "schedule_success")
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	gitForExecutionTest(t, s.options.Database.Root, "worktree", "add", "-b", "review-linked", linkedRoot)
	identity, err := discovery.FindGitIdentity(context.Background(), linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	rootPath, err := discovery.NormalizeLocalPath(s.options.Database.Root, identity.WorkTreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := discovery.NormalizeLocalPath(s.options.Database.Root, identity.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := sqlite.OpenProjectDatabase(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = projects.RegisterProject(context.Background(), storage.ProjectRegistration{Code: request.Selected.Project.Code, GitCommonDir: request.Selected.Project.GitCommonDir, GitDir: gitDir, WorkspaceRoot: rootPath})
	if err != nil {
		projects.Close()
		t.Fatal(err)
	}
	linked, err := projects.FindWorkspaceByGitDir(context.Background(), gitDir)
	closeErr := projects.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("resolve linked worktree: %v %v", err, closeErr)
	}
	if _, err := s.options.Supervisor.options.Settings.Save(context.Background(), s.options.Database, storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: linked.Workspace.ID, Settings: storage.CodexRunSettings{Executable: executable, Model: "test-model"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linkedRoot, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	linkedScheduler := NewScheduler(context.Background(), SchedulerOptions{Database: s.options.Database, Supervisor: s.options.Supervisor, OpenQueue: s.options.OpenQueue})
	t.Cleanup(func() { linkedScheduler.Close() })
	linkedStatus := awaitSchedule(t, startSchedule(t, linkedScheduler, ScheduleRequest{Selected: linked, Mode: "run_one"}))
	if linkedStatus.State != "completed" {
		t.Fatalf("linked implementation: %+v", linkedStatus)
	}
	linkedRun, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, linkedStatus.RunID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.CreatePellet(context.Background(), request.Selected, storage.NewPellet{Title: "main target"})
	if err != nil {
		t.Fatal(err)
	}
	mainStatus := awaitSchedule(t, startSchedule(t, s, request))
	if mainStatus.State != "completed" {
		t.Fatalf("main implementation: %+v", mainStatus)
	}
	mainRun, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, mainStatus.RunID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := queue.CreatePellet(context.Background(), request.Selected, storage.NewPellet{Title: "Cross-worktree review", Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{{ProjectCode: request.Selected.Project.Code, Number: 1}, second.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("review_clean"), 0600); err != nil {
		t.Fatal(err)
	}
	s.options.Checkpoints = NewCheckpointReviewPolicy(s.options.Database, s.options.OpenQueue)
	reviewStatus := awaitSchedule(t, startSchedule(t, s, request))
	if reviewStatus.State != "completed" {
		t.Fatalf("cross-worktree review: %+v", reviewStatus)
	}
	reviewRun, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, reviewStatus.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewRun.PelletNumber != checkpoint.Reference.Number || reviewRun.ReviewSnapshot == nil || len(reviewRun.ReviewSnapshot.Commits) != 2 || reviewRun.ReviewSnapshot.Commits[0].WorkspaceID != linked.Workspace.ID || reviewRun.ReviewSnapshot.Commits[0].ResultCommit != linkedRun.ResultCommit || reviewRun.ReviewSnapshot.Commits[1].WorkspaceID != request.Selected.Workspace.ID || reviewRun.ReviewSnapshot.Commits[1].ResultCommit != mainRun.ResultCommit {
		t.Fatalf("cross-worktree exact scope: %#v", reviewRun.ReviewSnapshot)
	}
}

func TestCheckpointReviewerRejectsMissingResultAndSideEffects(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct{ mode, code string }{{"review_same_context", "review_start_unconfirmed"}, {"review_missing_result", "review_result_invalid"}, {"review_malformed_result", "review_result_invalid"}, {"review_json_result", "review_result_invalid"}, {"review_fallback_prose", "review_result_invalid"}, {"review_truncated_json", "review_result_invalid"}, {"review_fenced_malformed", "review_result_invalid"}, {"review_side_effect", "review_side_effect_detected"}, {"review_dirty_side_effect", "review_side_effect_detected"}, {"review_interaction", "review_interaction_forbidden"}} {
		t.Run(test.mode, func(t *testing.T) {
			s, request, checkpoint, _ := preparedReviewCheckpoint(t, executable, test.mode)
			if test.mode == "review_dirty_side_effect" {
				if err := os.WriteFile(filepath.Join(s.options.Database.Root, "code-1.txt"), []byte("dirty-before\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "needs_attention" || status.Reason != test.code {
				t.Fatalf("failure status: %+v", status)
			}
			queue, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
			if err != nil {
				t.Fatal(err)
			}
			pellet, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
			err = errorsJoin(readErr, queue.Close())
			if err != nil || pellet.Status != domain.PelletInProgress {
				t.Fatalf("failed review advanced checkpoint: %+v %v", pellet, err)
			}
		})
	}
}

func TestCheckpointReviewerStopsBeforeCodexWhenRecordedCommitObjectIsMissing(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, checkpoint, implementations := preparedReviewCheckpoint(t, executable, "review_clean")
	missing := implementations[0].ResultCommit
	object := filepath.Join(s.options.Database.Root, ".git", "objects", missing[:2], missing[2:])
	if err := os.Remove(object); err != nil {
		t.Fatalf("remove disposable loose object: %v", err)
	}
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "needs_attention" || status.Reason != "commit_unavailable" {
		t.Fatalf("missing evidence status: %+v", status)
	}
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == string(codex.ReviewStart) {
			t.Fatal("review started after exact commit evidence disappeared")
		}
	}
	queue, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	pellet, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
	err = errorsJoin(readErr, queue.Close())
	if err != nil || pellet.Status != domain.PelletInProgress {
		t.Fatalf("missing evidence advanced checkpoint: %+v %v", pellet, err)
	}
}

func TestCheckpointReviewerCancellationResumesDurableResultWithoutRepeatingReview(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _, _ := preparedReviewCheckpoint(t, executable, "review_result_gate")
	h := startSchedule(t, s, request)
	var first storage.ExecutionRun
	deadline := time.Now().Add(15 * time.Second)
	for {
		runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 1)
		if err == nil && len(runs) == 1 && runs[0].ReviewResult != nil {
			first = runs[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("review result was not persisted: %#v %v", runs, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.StopNow()
	_ = awaitSchedule(t, h)
	first, _ = s.options.Supervisor.ReadRun(context.Background(), s.options.Database, first.ID)
	if first.State != "interrupted" || first.ReviewResult == nil {
		t.Fatalf("cancelled evidence: %#v", first)
	}
	initialTurns := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == string(codex.TurnStart) {
			initialTurns++
		}
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("review_clean"), 0600); err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &first.ID, &first.PelletNumber
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "completed" {
		t.Fatalf("resume status: %+v", status)
	}
	count, turns := 0, 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == string(codex.ReviewStart) {
			count++
			var params struct{ Target struct{ Instructions string } }
			if err := json.Unmarshal(event.Params, &params); err != nil {
				t.Fatal(err)
			}
			assertCapturedCheckpointPrefix(t, params.Target.Instructions, first.PromptPrefix.Text, "Review exactly the immutable checkpoint")
		}
		if event.Method == string(codex.TurnStart) {
			turns++
		}
	}
	if count != 1 || turns != initialTurns {
		t.Fatalf("review resume repeated work: review starts=%d turns=%d want=%d", count, turns, initialTurns)
	}
}

func errorsJoin(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
