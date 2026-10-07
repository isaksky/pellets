package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pellets/internal/storage"
)

func TestAsyncQuestionRemainsAnswerableAfterTurnCompletion(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_async_question")
	h := startSchedule(t, s, request)
	var run storage.ExecutionRun
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 {
			run = runs[0]
			if run.Interaction != nil || !storage.RunActive(run.State) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Interaction == nil {
		t.Fatalf("async multiple-choice question was lost at turn completion: state=%s error=%s", run.State, run.ErrorCode)
	}
	if len(run.Interaction.Questions) != 1 || len(run.Interaction.Questions[0].Options) != 2 {
		t.Fatalf("question choices lost: %+v", run.Interaction)
	}
	// Wait until the original turn has ended: answering must start a turn with
	// the user's answer, never start an unqualified continuation first.
	deadline = time.Now().Add(5 * time.Second)
	for {
		s.options.Supervisor.mu.Lock()
		execution := s.options.Supervisor.active[activeExecutionKey{databasePath: s.options.Database.Path, runID: run.ID}]
		s.options.Supervisor.mu.Unlock()
		current, _ := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, run.ID)
		if execution != nil && completedTurn(execution.LatestCompletion(), run.ThreadID, run.TurnID) && current.State == "awaiting_input" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("original turn did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ = s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, run.ID)
	_, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, RequestID: run.Interaction.RequestID, Action: "answer", Answers: map[string][]string{run.Interaction.Questions[0].ID: {"Skip Windows verification"}}})
	if err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, h); status.Completed != 1 {
		t.Fatalf("answer did not complete exact run: %+v", status)
	}
}

func TestAsyncQuestionSurvivesStopAndRequiresAnswerBeforeResume(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_async_question")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, true)
	h.StopNow()
	awaitSchedule(t, h)
	run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, run.ID)
	if err != nil || run.Interaction == nil {
		t.Fatalf("stop lost question: %+v %v", run, err)
	}
	request.ResumeFrom, request.ResumePellet = &run.ID, &run.PelletNumber
	if _, err = s.Start(context.Background(), request); err == nil {
		t.Fatal("bare Resume must not answer or grant permission")
	}
	request.ResumeAnswer = &InteractionSubmission{RunID: run.ID, Revision: run.Revision - 1, RequestID: run.Interaction.RequestID, Action: "answer", Answers: map[string][]string{run.Interaction.Questions[0].ID: {"Skip Windows verification"}}}
	if _, err = s.Start(context.Background(), request); err == nil {
		t.Fatal("accepted stale answer")
	}
	request.ResumeAnswer.Revision = run.Revision
	if status := awaitSchedule(t, startSchedule(t, s, request)); status.Completed != 1 {
		t.Fatalf("answer and resume failed: %+v", status)
	}
	var starts []string
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			starts = append(starts, string(event.Params))
		}
	}
	if len(starts) != 2 || !strings.Contains(starts[1], "User answers") || !strings.Contains(starts[1], "Skip Windows verification") {
		t.Fatalf("answer was not part of the first resumed turn: %#v", starts)
	}
}

func TestResumeMessagePrecedesFirstResumedTurn(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_unfinished")
	awaitSchedule(t, startSchedule(t, s, request))
	runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	run := runs[0]
	request.ResumeFrom, request.ResumePellet = &run.ID, &run.PelletNumber
	request.ResumeMessage = "Do not upload anything. Verify locally only."
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_noop"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, startSchedule(t, s, request)); status.Completed != 1 {
		t.Fatalf("resume failed: %+v", status)
	}
	var starts []string
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			starts = append(starts, string(event.Params))
		}
	}
	if len(starts) != 2 || !strings.Contains(starts[1], request.ResumeMessage) {
		t.Fatalf("message missing from first resumed turn: %#v", starts)
	}
}

func TestAsyncQuestionAnswerSteersExactLiveTurn(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_async_live")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, true)
	_, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, RequestID: run.Interaction.RequestID, Action: "answer", Answers: map[string][]string{run.Interaction.Questions[0].ID: {"Use my local checkout; do not upload."}}})
	if err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, h); status.Completed != 1 {
		t.Fatalf("live answer failed: %+v", status)
	}
	starts, steers := 0, 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			starts++
		}
		if event.Method == "turn/steer" {
			steers++
			if !strings.Contains(string(event.Params), "Use my local checkout; do not upload.") {
				t.Fatal("free-text answer was lost")
			}
		}
	}
	if starts != 1 || steers != 1 {
		t.Fatalf("answer started another turn: starts=%d steers=%d", starts, steers)
	}
}

func TestAsyncQuestionsAccumulateWithoutDroppingEarlierChoices(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_async_two")
	h := startSchedule(t, s, request)
	var run storage.ExecutionRun
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) > 0 {
			run = runs[0]
			if run.State == "awaiting_input" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Interaction == nil || len(run.Interaction.Questions) != 2 {
		t.Fatalf("lost successive questions: %+v", run)
	}
	answers := map[string][]string{run.Interaction.Questions[0].ID: {"Skip Windows verification"}, run.Interaction.Questions[1].ID: {"Keep the local changes."}}
	_, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, RequestID: run.Interaction.RequestID, Action: "answer", Answers: answers})
	if err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, h); status.Completed != 1 {
		t.Fatalf("combined answer failed: %+v", status)
	}
}
