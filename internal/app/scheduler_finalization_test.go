package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type finalizationFailureDatabase struct {
	storage.ExecutionRunDatabase
	fail func(storage.UpdateExecutionRun) bool
}

func (db finalizationFailureDatabase) UpdateExecutionRun(ctx context.Context, update storage.UpdateExecutionRun) (storage.ExecutionRun, error) {
	if db.fail(update) {
		return storage.ExecutionRun{}, errors.New("injected durable boundary failure")
	}
	return db.ExecutionRunDatabase.UpdateExecutionRun(ctx, update)
}

func TestSchedulerFinalizationRecoveryNeverReimplementsOrRecommits(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, boundary := range []string{"before_staging", "after_commit", "before_close", "after_close"} {
		t.Run(boundary, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, "schedule_success")
			originalOpen := s.options.Supervisor.options.Recorder.Open
			var failed atomic.Bool
			s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
				db, err := originalOpen(ctx, path)
				if err != nil {
					return nil, err
				}
				return finalizationFailureDatabase{ExecutionRunDatabase: db, fail: func(update storage.UpdateExecutionRun) bool {
					atBoundary := boundary == "before_staging" && update.Progress.Phase == "commit" || boundary == "after_commit" && update.VerifiedCommit != "" || boundary == "before_close" && update.Progress.Phase == "close" || boundary == "after_close" && update.Progress.State == "completed"
					return atBoundary && failed.CompareAndSwap(false, true)
				}}, nil
			}
			first := awaitSchedule(t, startSchedule(t, s, request))
			if first.State != "needs_attention" || !failed.Load() {
				t.Fatalf("first = %+v", first)
			}
			previous, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
			if err != nil || previous.Finalization == nil {
				t.Fatalf("durable intent: %+v %v", previous, err)
			}
			if boundary == "after_commit" && previous.ResultCommit != "" {
				t.Fatal("failure did not precede commit evidence save")
			}
			p, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: previous.PelletNumber})
			if err != nil {
				t.Fatal(err)
			}
			if (p.Status == domain.PelletClosed) != (boundary == "after_close") {
				t.Fatalf("wrong queue boundary: %+v", p)
			}
			// A changed runtime behavior proves recovery cannot depend on a
			// second successful implementation turn or repeat its tests.
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_failed"), 0600); err != nil {
				t.Fatal(err)
			}
			request.ResumePellet, request.ResumeFrom = &previous.PelletNumber, &previous.ID
			second := awaitSchedule(t, startSchedule(t, s, request))
			if second.State != "completed" || second.Completed != 1 {
				t.Fatalf("recovery = %+v; supervisor error = %v", second, s.options.Supervisor.err)
			}
			resumed, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, second.RunID)
			if err != nil || resumed.StartingHead != previous.StartingHead || resumed.ResultCommit == "" || resumed.Finalization == nil {
				t.Fatalf("resumed evidence: %+v %v", resumed, err)
			}
			if previous.ResultCommit != "" && resumed.ResultCommit != previous.ResultCommit {
				t.Fatal("recovery substituted verified commit identity")
			}
			if count := gitForExecutionTest(t, s.options.Database.Root, "rev-list", "--count", previous.StartingHead+"..HEAD"); count != "1" {
				t.Fatalf("commit count = %s", count)
			}
			turns := 0
			for _, event := range readPeerEvents(t, s.options.Database.Root) {
				if event.Method == "turn/start" {
					turns++
				}
			}
			if turns != 1 {
				t.Fatalf("implementation repeated %d times", turns)
			}
		})
	}
}

func TestSchedulerRejectsDirtyNewWorkWithoutSelecting(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, q := schedulerFixture(t, executable, "schedule_success")
	file := filepath.Join(s.options.Database.Root, "unrelated.txt")
	if err := os.WriteFile(file, []byte("preserve\n"), 0600); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "needs_attention" || status.Reason != "implementation_worktree_dirty" || status.Started != 0 {
		t.Fatalf("dirty start = %+v", status)
	}
	p, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
	if err != nil || p.Status != domain.PelletOpen {
		t.Fatalf("dirty selection mutated queue: %+v %v", p, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "preserve\n" {
		t.Fatalf("unrelated content changed: %q %v", data, err)
	}
}

func TestSchedulerRejectsWrongReportsAndInterference(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct{ mode, code string }{
		{"schedule_wrong_report", "implementation_report_invalid"},
		{"schedule_noop", "implementation_no_changes"},
		{"schedule_unreported_file", "implementation_files_changed"},
		{"schedule_staged", "implementation_index_changed"},
		{"schedule_commit_only", "implementation_head_changed"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, test.mode)
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "needs_attention" || status.Reason != test.code {
				t.Fatalf("wrong acceptance: %+v", status)
			}
			p, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
			if err != nil || p.Status != domain.PelletInProgress {
				t.Fatalf("work released/closed: %+v %v", p, err)
			}
			if test.mode == "schedule_unreported_file" {
				if data, err := os.ReadFile(filepath.Join(s.options.Database.Root, "unrelated.txt")); err != nil || string(data) != "preserve" {
					t.Fatal("interference lost")
				}
			}
		})
	}
}

func TestFinalizationLiteralPathsAndMetadataBoundary(t *testing.T) {
	for _, file := range []string{"../escape", "/absolute", ".pellets/a", "nested/.pellets/data", ".git/config", "a/../b", ".", "a\\b"} {
		if validateImplementationFiles([]string{file}) == nil {
			t.Fatalf("accepted %q", file)
		}
	}
	_, database, _, _ := executionFixture(t)
	files := []string{" leading space.txt", ":(glob)*.txt", "with\nnewline.txt"}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(database.Root, file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(database.Root, ".git", "info", "exclude"), []byte("/pellets.db*\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := requireExactFiles(context.Background(), database.Root, files); err != nil {
		t.Fatal(err)
	}
	head := gitForExecutionTest(t, database.Root, "rev-parse", "HEAD")
	tree, err := implementationTree(context.Background(), database.Root, head, files)
	if err != nil || !storage.IsFullCommitID(tree) {
		t.Fatalf("literal tree = %s %v", tree, err)
	}
	if staged := gitForExecutionTest(t, database.Root, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("temporary index changed real index: %q", staged)
	}
	output := gitForExecutionTest(t, database.Root, "ls-tree", "-r", "--name-only", tree)
	if !strings.Contains(output, ":(glob)*.txt") {
		t.Fatalf("literal path missing: %q", output)
	}
}

func TestSchedulerDetectsOwnershipAndHeadInterference(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, action := range []string{"release", "commit"} {
		t.Run(action, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, "schedule_gate")
			h := startSchedule(t, s, request)
			awaitScheduledTurn(t, s)
			if action == "release" {
				if _, err := q.TransitionPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}, storage.PelletLifecycleRequest{Operation: storage.PelletRelease}); err != nil {
					t.Fatal(err)
				}
			} else {
				gitForExecutionTest(t, s.options.Database.Root, "commit", "--allow-empty", "-m", "external change")
			}
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-complete"), []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			status := awaitSchedule(t, h)
			code := "implementation_ownership_changed"
			if action == "commit" {
				code = "implementation_head_changed"
			}
			if status.State != "needs_attention" || status.Reason != code {
				t.Fatalf("interference = %+v", status)
			}
			if _, err := os.Stat(filepath.Join(s.options.Database.Root, "demo-1.txt")); err != nil {
				t.Fatal("implementation change was discarded", err)
			}
		})
	}
}

func TestFinalizationRecoveryRejectsExtraCommit(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_success")
	open := s.options.Supervisor.options.Recorder.Open
	var failed atomic.Bool
	s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		db, err := open(ctx, path)
		if err != nil {
			return nil, err
		}
		return finalizationFailureDatabase{ExecutionRunDatabase: db, fail: func(update storage.UpdateExecutionRun) bool {
			return update.VerifiedCommit != "" && failed.CompareAndSwap(false, true)
		}}, nil
	}
	first := awaitSchedule(t, startSchedule(t, s, request))
	previous, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gitForExecutionTest(t, s.options.Database.Root, "commit", "--allow-empty", "-m", "external commit after implementation")
	head := gitForExecutionTest(t, s.options.Database.Root, "rev-parse", "HEAD")
	request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "needs_attention" || status.Reason != "finalization_parent_changed" {
		t.Fatalf("conflicting recovery = %+v", status)
	}
	if got := gitForExecutionTest(t, s.options.Database.Root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("recovery changed head: %s", got)
	}
}

func TestCheckpointPolicyBypassesOrdinaryFinalizer(t *testing.T) {
	recorder, database, selected, capture := executionFixture(t)
	capture.Mode = "review_checkpoint"
	run, err := recorder.Begin(context.Background(), database, selected, capture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(database.Root, "review-notes.txt"), []byte("existing review context"), 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("separate review policy")
	s := &Scheduler{options: SchedulerOptions{Database: database, Checkpoints: &CheckpointExecutionPolicy{
		Drive:              func(context.Context, *WorkspaceExecution) error { return sentinel },
		ValidateCompletion: func(context.Context, storage.ExecutionRun, storage.ResolvedProject) error { return sentinel },
	}}}
	execution := &WorkspaceExecution{recorder: recorder, database: database, id: run.ID}
	if err := s.drive(context.Background(), execution); !errors.Is(err, sentinel) {
		t.Fatalf("checkpoint used implementation driver: %v", err)
	}
	if err := s.validateCompletion(context.Background(), run, selected); !errors.Is(err, sentinel) {
		t.Fatalf("checkpoint used implementation commit policy: %v", err)
	}
	if head := gitForExecutionTest(t, database.Root, "rev-parse", "HEAD"); head != run.StartingHead {
		t.Fatal("checkpoint manufactured a commit")
	}
}

func TestFinalizationCommitHookFailurePreservesStageAndRecoversOnce(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_success")
	hook := filepath.Join(s.options.Database.Root, ".git", "hooks", "pre-commit")
	script := "#!/bin/sh\nprintf 'token=" + strings.Repeat("FAKE-LONG-CREDENTIAL", 1024) + "\\n' >&2\nprintf '\\033[31mHOOK-DIAGNOSTIC signing key unavailable\\033[0m token=do-not-persist\\n' >&2\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "needs_attention" || first.Reason != "finalization_commit_unconfirmed" {
		t.Fatalf("hook failure = %+v", first)
	}
	if staged := gitForExecutionTest(t, s.options.Database.Root, "diff", "--cached", "--name-only"); staged != "demo-1.txt" {
		t.Fatalf("stage lost: %q", staged)
	}
	previous, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(previous.Summary, "HOOK-DIAGNOSTIC signing key unavailable") || strings.Contains(previous.Summary, "do-not-persist") || strings.Contains(previous.Summary, "CREDENTIAL") || strings.ContainsRune(previous.Summary, '\x1b') {
		t.Fatalf("failure diagnostic lost/unsanitized: %q", previous.Summary)
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &previous.ID, &previous.PelletNumber
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "completed" {
		t.Fatalf("stage recovery = %+v", status)
	}
	if count := gitForExecutionTest(t, s.options.Database.Root, "rev-list", "--count", previous.StartingHead+"..HEAD"); count != "1" {
		t.Fatalf("commit count = %s", count)
	}
	evidence, err := s.options.Supervisor.options.Recorder.InspectEvidence(context.Background(), s.options.Database, previous.ID, nil)
	if err != nil || evidence.Run.Summary != previous.Summary {
		t.Fatalf("recovery inspection lost failure diagnostic: %+v %v", evidence, err)
	}
}

func TestFinalizationRejectsHookChangedCommit(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, q := schedulerFixture(t, executable, "schedule_success")
	hook := filepath.Join(s.options.Database.Root, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf 'unexpected hook change' > hook-file.txt\ngit add -- hook-file.txt\n"), 0700); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "needs_attention" || status.Reason != "finalization_tree_changed" {
		t.Fatalf("hook mutation = %+v", status)
	}
	p, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
	if err != nil || p.Status != domain.PelletInProgress {
		t.Fatalf("wrong commit closed pellet: %+v %v", p, err)
	}
	if data, err := os.ReadFile(filepath.Join(s.options.Database.Root, "hook-file.txt")); err != nil || string(data) != "unexpected hook change" {
		t.Fatal("hook diagnostics discarded")
	}
}
