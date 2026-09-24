package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
)

func TestAdmissionRequiresStartingCommitBeforeClaim(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"run_one", "drain", "watch"} {
		for _, files := range []string{"empty", "untracked"} {
			t.Run(mode+"/"+files, func(t *testing.T) {
				s, request, q := schedulerFixture(t, executable, "schedule_success")
				ctx, root := context.Background(), s.options.Database.Root
				request.Mode = mode
				gitForExecutionTest(t, root, "symbolic-ref", "HEAD", "refs/heads/unborn")
				if files == "untracked" {
					if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("Planning notes\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before, err := q.ReadPellet(ctx, request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
				if err != nil {
					t.Fatal(err)
				}
				public := domain.PublicError(s.CheckAdmission(ctx, request))
				if public.Code != "starting_head_unavailable" || !strings.Contains(public.Message, "initial Git commit") {
					t.Fatalf("admission error = %+v", public)
				}
				// Noninteractive starts and Watch/Drain must enforce the same rule.
				status := awaitSchedule(t, startSchedule(t, s, request))
				if status.State != "needs_attention" || status.Reason != public.Code || status.Detail != public.Message {
					t.Fatalf("schedule result = %+v", status)
				}
				after, err := q.ReadPellet(ctx, request.Selected, before.Reference)
				if err != nil || after.Status != domain.PelletOpen || after.Workspace != nil || after.ImplementationRevision != before.ImplementationRevision || after.UpdatedAt != before.UpdatedAt {
					t.Fatalf("failed admission changed pellet: before=%+v after=%+v error=%v", before, after, err)
				}
				runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(ctx, s.options.Database, request.Selected.Workspace.ID, 10)
				if err != nil || len(runs) != 0 {
					t.Fatalf("failed admission created runs: %+v %v", runs, err)
				}
				owner, err := executionlock.ReadOwner(filepath.Join(root, ".git"))
				if err != nil || owner != nil {
					t.Fatalf("failed admission left recovery receipt: %+v %v", owner, err)
				}
				if _, err := os.Stat(filepath.Join(root, "fake-events.jsonl")); !os.IsNotExist(err) {
					t.Fatalf("repository check must precede runtime startup: %v", err)
				}
			})
		}
	}
}

func TestSchedulerRechecksStartingCommitBeforeClaim(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, when := range []string{"after_interactive_admission", "during_selection"} {
		t.Run(when, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, "schedule_success")
			if err := s.CheckAdmission(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			makeUnborn := func() { gitForExecutionTest(t, s.options.Database.Root, "symbolic-ref", "HEAD", "refs/heads/unborn") }
			if when == "after_interactive_admission" {
				makeUnborn()
			} else {
				checks := 0
				s.options.Ready = func(context.Context, storage.ResolvedProject, storage.Pellet) (bool, error) {
					checks++
					if checks == 2 {
						makeUnborn()
					}
					return true, nil
				}
			}
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.Reason != "starting_head_unavailable" {
				t.Fatalf("schedule result = %+v", status)
			}
			p, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
			if err != nil || p.Status != domain.PelletOpen || p.Workspace != nil {
				t.Fatalf("claimed work after HEAD changed: %+v %v", p, err)
			}
		})
	}
}

func TestAdmissionInitialCommitAllowsRetryAndNoRunResume(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, resume := range []bool{false, true} {
		name := "new_work"
		if resume {
			name = "existing_no_run_ownership"
		}
		t.Run(name, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, "schedule_success")
			ctx, root := context.Background(), s.options.Database.Root
			ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
			if resume {
				if _, err := q.TransitionPellet(ctx, request.Selected, ref, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
					t.Fatal(err)
				}
				request.ResumePellet = &ref.Number
			}
			gitForExecutionTest(t, root, "symbolic-ref", "HEAD", "refs/heads/unborn")
			if err := s.CheckAdmission(ctx, request); domain.PublicError(err).Code != "starting_head_unavailable" {
				t.Fatalf("admission = %v", err)
			}
			gitForExecutionTest(t, root, "commit", "--allow-empty", "-m", "Initial commit")
			if err := s.CheckAdmission(ctx, request); err != nil {
				t.Fatal(err)
			}
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "completed" || status.Completed != 1 {
				t.Fatalf("retry result = %+v", status)
			}
		})
	}
}

func TestAdmissionRejectsBeforeClaimOrConversation(t *testing.T) {
	for _, mode := range []string{"runtime_old", "unauthenticated"} {
		t.Run(mode, func(t *testing.T) {
			s, request, q := schedulerFixture(t, installSupervisorPeer(t), mode)
			if err := s.CheckAdmission(context.Background(), request); err == nil {
				t.Fatal("invalid admission succeeded")
			}
			p, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
			if err != nil || p.Status != domain.PelletOpen {
				t.Fatalf("preflight claimed work: %+v %v", p, err)
			}
			runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 10)
			if err != nil || len(runs) != 0 {
				t.Fatalf("preflight created attempt: %+v %v", runs, err)
			}
			if _, err := os.Stat(filepath.Join(s.options.Database.Root, "fake-events.jsonl")); err == nil {
				for _, event := range readPeerEvents(t, s.options.Database.Root) {
					if event.Method == "thread/start" || event.Method == "turn/start" {
						t.Fatal("preflight started conversation")
					}
				}
			}
		})
	}
}

func TestAdmissionFreshConversationChoiceSurvivesExecution(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_unfinished")
	first := awaitSchedule(t, startSchedule(t, s, request))
	prior, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &prior.ID, &prior.PelletNumber
	modeFile := filepath.Join(s.options.Database.Root, "fake-mode")
	if err := os.WriteFile(modeFile, []byte("schedule_missing_history"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckAdmission(context.Background(), request); err == nil {
		t.Fatal("missing history accepted without choice")
	}
	request.FreshConversation = true
	if err := s.CheckAdmission(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modeFile, []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	result := awaitSchedule(t, startSchedule(t, s, request))
	if result.State != "completed" {
		t.Fatalf("choice lost: %+v", result)
	}
	starts, resumes := 0, 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "thread/start" {
			starts++
		}
		if event.Method == "thread/resume" {
			resumes++
		}
	}
	if starts != 2 || resumes != 0 {
		t.Fatalf("starts=%d resumes=%d", starts, resumes)
	}
	old, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, prior.ID)
	if err != nil || old.Summary != prior.Summary {
		t.Fatal("old attempt changed")
	}
}

func TestAdmissionStopsWithSupervisorWithoutClaiming(t *testing.T) {
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_success")
	entered := make(chan struct{})
	s.options.Supervisor.options.Prepare = func(ctx context.Context, _ codex.PrepareOptions) (*codex.PreparedRun, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- s.CheckAdmission(context.Background(), request) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("preflight did not start")
	}
	s.options.Supervisor.StopScheduling()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled preflight succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preflight ignored shutdown")
	}
	pellet, err := q.ReadPellet(context.Background(), request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1})
	if err != nil || pellet.Status != domain.PelletOpen {
		t.Fatalf("preflight claimed work: %+v %v", pellet, err)
	}
}
