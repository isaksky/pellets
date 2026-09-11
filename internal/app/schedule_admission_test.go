package app

import (
	"context"
	"os"
	"path/filepath"
	"pellets/internal/codex"
	"testing"
	"time"

	"pellets/internal/domain"
)

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
