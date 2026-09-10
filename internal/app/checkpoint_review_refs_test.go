package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func commitReviewOtherWorktree(t *testing.T, root string) string {
	t.Helper()
	file := filepath.Join(root, "independent-work.txt")
	contents, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(contents, []byte("independent work\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	gitForExecutionTest(t, root, "add", "--", "independent-work.txt")
	return gitForExecutionTest(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "independent work")
}

func TestCheckpointReviewConcurrentWorktreeCommitCompletesAndResumes(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, phase := range []string{"review", "triage"} {
		for _, resume := range []bool{false, true} {
			name := phase + "/complete"
			if resume {
				name = phase + "/resume"
			}
			t.Run(name, func(t *testing.T) {
				mode := "review_result_gate"
				if phase == "triage" {
					mode = "review_findings_triage_gate"
				}
				s, request, checkpoint, implementations := preparedReviewCheckpoint(t, executable, mode)
				root := s.options.Database.Root
				other := filepath.Join(t.TempDir(), "other worktree")
				gitForExecutionTest(t, root, "worktree", "add", "-b", "independent", other)
				h := startSchedule(t, s, request)
				var first storage.ExecutionRun
				deadline := time.Now().Add(15 * time.Second)
				for {
					runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 1)
					_, gateErr := os.Stat(filepath.Join(root, "fake-triage-ready"))
					if err == nil && len(runs) == 1 && runs[0].ReviewResult != nil && (phase == "review" || gateErr == nil) {
						first = runs[0]
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("phase did not reach gate: %#v %v", runs, err)
					}
					time.Sleep(5 * time.Millisecond)
				}
				before := gitForExecutionTest(t, root, "rev-parse", "HEAD")
				commitReviewOtherWorktree(t, other)
				commitReviewOtherWorktree(t, other)
				if resume {
					h.StopNow()
					_ = awaitSchedule(t, h)
					request.ResumeFrom, request.ResumePellet = &first.ID, &first.PelletNumber
				}
				if err := os.WriteFile(filepath.Join(root, "fake-complete"), []byte("go"), 0600); err != nil {
					t.Fatal(err)
				}
				if resume {
					h = startSchedule(t, s, request)
				}
				status := awaitSchedule(t, h)
				if status.State != "completed" {
					t.Fatalf("concurrent commit blocked %s: %+v error=%v", name, status, s.options.Supervisor.err)
				}
				run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
				if err != nil || !storage.CompletedReviewReceipt(run) || run.ReviewSnapshot.Commits[0].ResultCommit != implementations[0].ResultCommit || run.ReviewSnapshot.Commits[1].ResultCommit != implementations[2].ResultCommit {
					t.Fatalf("exact receipt changed: %+v %v", run, err)
				}
				if after := gitForExecutionTest(t, root, "rev-parse", "HEAD"); after != before {
					t.Fatalf("review HEAD moved: %s -> %s", before, after)
				}
				queue, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
				if err != nil {
					t.Fatal(err)
				}
				pellet, readErr := queue.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
				err = errorsJoin(readErr, queue.Close())
				if err != nil || pellet.Status != domain.PelletClosed {
					t.Fatalf("checkpoint did not close: %+v %v", pellet, err)
				}
				reviews := 0
				for _, event := range readPeerEvents(t, root) {
					if event.Method == "review/start" {
						reviews++
					}
				}
				if reviews != 1 {
					t.Fatalf("review replayed %d times", reviews)
				}
			})
		}
	}
}

func TestCheckpointReviewRefAttributionFailsClosed(t *testing.T) {
	for _, mode := range []string{"commit", "capture_race", "verify_race", "new_ref", "tag", "replace", "foreign_update_ref", "foreign_rewind_intermediate", "foreign_rewind_baseline", "capture_historical_tip", "foreign_reset", "missing_log", "rewritten_log", "unattached_ref", "head_binding", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			gitForExecutionTest(t, root, "init", "-b", "review")
			commitReviewOtherWorktree(t, root)
			base := gitForExecutionTest(t, root, "rev-parse", "HEAD")
			other := filepath.Join(t.TempDir(), "linked")
			gitForExecutionTest(t, root, "worktree", "add", "-b", "other", other)
			commitReviewOtherWorktree(t, other)
			gitForExecutionTest(t, root, "branch", "unattached", base)
			if mode == "missing_log" {
				gitDir := gitForExecutionTest(t, other, "rev-parse", "--absolute-git-dir")
				if err := os.Remove(filepath.Join(gitDir, "logs", "HEAD")); err != nil {
					t.Fatal(err)
				}
			}
			_, _, refs, err := reviewRepositoryState(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "capture_race" {
				commitReviewOtherWorktree(t, other)
			}
			if mode == "capture_historical_tip" {
				original := gitForExecutionTest(t, other, "rev-parse", "HEAD")
				commitReviewOtherWorktree(t, other)
				gitForExecutionTest(t, root, "update-ref", "refs/heads/other", original)
			}
			guard, refs, err := captureReviewRefContext(context.Background(), root, refs)
			if mode == "capture_historical_tip" {
				var failure *domain.Error
				if !errors.As(err, &failure) || failure.Code != "review_repository_state_unavailable" {
					t.Fatalf("capture accepted a historical reflog tip: %+v %v", guard, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := &storage.ReviewSnapshot{RepositoryRefsSHA256: reviewRefsDigest(refs), RepositoryRefContext: guard}
			switch mode {
			case "commit", "capture_race", "verify_race", "missing_log", "rewritten_log", "legacy":
				commitReviewOtherWorktree(t, other)
				if mode == "rewritten_log" {
					gitForExecutionTest(t, other, "reflog", "expire", "--expire=all", "HEAD")
				}
				if mode == "legacy" {
					snapshot.RepositoryRefContext = nil
				}
			case "new_ref":
				gitForExecutionTest(t, root, "update-ref", "refs/heads/created-by-reviewer", base)
			case "tag":
				gitForExecutionTest(t, root, "tag", "reviewer-tag", base)
			case "replace":
				gitForExecutionTest(t, root, "replace", base, gitForExecutionTest(t, other, "rev-parse", "HEAD"))
			case "foreign_update_ref":
				gitForExecutionTest(t, root, "update-ref", "refs/heads/other", base)
			case "foreign_rewind_intermediate", "foreign_rewind_baseline":
				original := gitForExecutionTest(t, other, "rev-parse", "HEAD")
				commitReviewOtherWorktree(t, other)
				rewind := gitForExecutionTest(t, other, "rev-parse", "HEAD")
				commitReviewOtherWorktree(t, other)
				if mode == "foreign_rewind_baseline" {
					rewind = original
				}
				gitForExecutionTest(t, root, "update-ref", "refs/heads/other", rewind)
			case "foreign_reset":
				gitForExecutionTest(t, other, "reset", "--soft", base)
			case "unattached_ref":
				gitForExecutionTest(t, root, "update-ref", "refs/heads/unattached", gitForExecutionTest(t, other, "rev-parse", "HEAD"))
			case "head_binding":
				gitForExecutionTest(t, root, "symbolic-ref", "HEAD", "refs/heads/unattached")
			}
			_, _, after, err := reviewRepositoryState(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "verify_race" {
				commitReviewOtherWorktree(t, other)
			}
			err = verifyReviewRefs(context.Background(), root, after, snapshot)
			if mode == "commit" || mode == "capture_race" || mode == "verify_race" {
				if err != nil {
					t.Fatalf("attributed commit rejected: %v", err)
				}
			} else {
				var failure *domain.Error
				if !errors.As(err, &failure) || failure.Code != "review_side_effect_detected" {
					t.Fatalf("ambiguous or forbidden ref change allowed: %v", err)
				}
			}
		})
	}
}

func TestCheckpointReviewRejectsForeignRewindAfterConcurrentCommits(t *testing.T) {
	s, request, _, _ := preparedReviewCheckpoint(t, installSupervisorPeer(t), "review_result_gate")
	root := s.options.Database.Root
	other := filepath.Join(t.TempDir(), "other")
	gitForExecutionTest(t, root, "worktree", "add", "-b", "independent", other)
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
			t.Fatalf("review result did not reach gate: %+v %v", runs, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	commitReviewOtherWorktree(t, other)
	intermediate := gitForExecutionTest(t, other, "rev-parse", "HEAD")
	commitReviewOtherWorktree(t, other)
	gitForExecutionTest(t, root, "update-ref", "refs/heads/independent", intermediate)
	if err := os.WriteFile(filepath.Join(root, "fake-complete"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, h)
	if status.State != "needs_attention" || status.Reason != "review_side_effect_detected" {
		t.Fatalf("review accepted unattributed rewind: %+v", status)
	}
	request.ResumeFrom, request.ResumePellet = &first.ID, &first.PelletNumber
	resumed := awaitSchedule(t, startSchedule(t, s, request))
	if resumed.State != "needs_attention" || resumed.Reason != "review_side_effect_detected" {
		t.Fatalf("Resume accepted unattributed rewind: %+v", resumed)
	}
}
