package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func schedulerFixture(t *testing.T, executable, mode string) (*Scheduler, ScheduleRequest, *sqlite.PelletRepository) {
	t.Helper()
	supervisor, execution := supervisorFixture(t, executable)
	if err := os.WriteFile(filepath.Join(execution.Database.Root, "fake-mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	q, err := sqlite.OpenPelletRepository(context.Background(), execution.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	if _, err := q.TransitionPellet(context.Background(), execution.Selected, domain.PelletReference{ProjectCode: execution.Selected.Project.Code, Number: 1}, storage.PelletLifecycleRequest{Operation: storage.PelletRelease}); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(context.Background(), SchedulerOptions{Database: execution.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}})
	t.Cleanup(func() { s.Close() })
	return s, ScheduleRequest{Selected: execution.Selected, Mode: "run_one"}, q
}

func awaitSchedule(t *testing.T, h *ScheduleHandle) ScheduleStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := h.Result(ctx)
	if err != nil {
		t.Fatalf("schedule timeout: %+v %v", h.Status(), err)
	}
	return status
}
func startSchedule(t *testing.T, s *Scheduler, r ScheduleRequest) *ScheduleHandle {
	t.Helper()
	h, err := s.Start(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func awaitScheduleState(t *testing.T, h *ScheduleHandle, state string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for h.Status().State != state {
		select {
		case <-h.done:
			t.Fatalf("schedule stopped: %+v", h.Status())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("state timeout: %+v", h.Status())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func awaitScheduledTurn(t *testing.T, s *Scheduler) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(s.options.Database.Root, "fake-events.jsonl"))
		if strings.Contains(string(data), `"method":"turn/start"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("turn timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitActiveRun(t *testing.T, s *Scheduler, interaction bool) storage.ExecutionRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, 1, 8)
		if err == nil && len(runs) > 0 && storage.RunActive(runs[0].State) && (runs[0].Interaction != nil) == interaction && (interaction || runs[0].Phase == "implementation") {
			return runs[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("active run timeout: %#v %v", runs, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConciseCodexActivityDoesNotExposeProtocolContent(t *testing.T) {
	for method, want := range map[string]string{
		"item/started":                          "Codex started the next workspace activity.",
		"item/completed":                        "Codex completed an activity; validating the bound result.",
		"item/commandExecution/requestApproval": "Automatic review routed this approval for an explicit human decision.",
		"item/tool/requestUserInput":            "Codex is awaiting explicit input.",
		"unknown/event":                         "",
	} {
		if got := conciseCodexActivity(method); got != want {
			t.Fatalf("summary for %q = %q, want %q", method, got, want)
		}
	}
}

func TestMCPFormInteractionUsesTypedBoundedControls(t *testing.T) {
	run := storage.ExecutionRun{RunProgress: storage.RunProgress{ThreadID: "thread", TurnID: "turn"}}
	params := json.RawMessage(`{"threadId":"thread","turnId":"turn","serverName":"example","mode":"form","message":"Choose deployment details","requestedSchema":{"type":"object","required":["count","safe"],"properties":{"safe":{"type":"boolean","title":"Safety","description":"Is this safe?"},"count":{"type":"integer","title":"Count","description":"How many?"},"label":{"type":"string","enum":["small","large"],"description":"Which size?"}}}}`)
	interaction, err := parseServerInteraction(codex.Event{ID: json.RawMessage(`"mcp-1"`), Method: "mcpServer/elicitation/request", Params: params}, run)
	if err != nil {
		t.Fatal(err)
	}
	if interaction.RequestID != `"mcp-1"` || interaction.Mode != "form" || len(interaction.Questions) != 3 || interaction.Questions[0].ID != "count" || interaction.Questions[0].Type != "integer" || !interaction.Questions[0].Required || interaction.Questions[2].Options[0].Label != "true" {
		t.Fatalf("MCP form interaction: %#v", interaction)
	}
}

func TestAutomaticReviewProgressAndDenialReasonAreExactlyCorrelatedAndBounded(t *testing.T) {
	event := codex.Event{Method: "item/autoApprovalReview/completed", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","review":{"status":"denied","rationale":"Credential probing is not authorized."}}`)}
	if got := conciseCodexEvent(event, "thread", "turn"); got != "Automatic approval review denied. Credential probing is not authorized." {
		t.Fatalf("review summary = %q", got)
	}
	if got := conciseCodexEvent(event, "other", "turn"); got != "" {
		t.Fatalf("cross-thread review surfaced: %q", got)
	}
}

func TestStrictAutomaticReviewProgressIsExactlyCorrelated(t *testing.T) {
	event := codex.Event{Method: "autoApprovalReview/strictReviewRequired", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","startedAtMs":1}`)}
	if got := conciseCodexEvent(event, "thread", "turn"); got != "Automatic approval review is in progress for each remaining command in this turn." {
		t.Fatalf("strict review progress = %q", got)
	}
	if got := conciseCodexEvent(event, "other-thread", "turn"); got != "" {
		t.Fatalf("cross-thread strict review progress = %q", got)
	}
}

func TestSchedulerRealDriverModesLimitAndExactBinding(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct {
		mode             string
		limit, completed int
		reason           string
	}{{"run_one", 0, 1, "run_one_complete"}, {"drain", 0, 3, "queue_empty"}, {"drain", 2, 2, "limit_reached"}, {"watch", 2, 2, "limit_reached"}} {
		t.Run(test.mode+test.reason, func(t *testing.T) {
			s, r, q := schedulerFixture(t, executable, "schedule_success")
			r.Mode, r.Limit = test.mode, test.limit
			for i := 0; i < 2; i++ {
				if _, err := q.CreatePellet(context.Background(), r.Selected, storage.NewPellet{Title: "next"}); err != nil {
					t.Fatal(err)
				}
			}
			status := awaitSchedule(t, startSchedule(t, s, r))
			if status.Completed != test.completed || status.Reason != test.reason {
				t.Fatalf("result: %+v", status)
			}
			if test.limit == 0 && status.Limit != 100 {
				t.Fatalf("default limit: %+v", status)
			}
			var turns int
			for _, event := range readPeerEvents(t, s.options.Database.Root) {
				if event.Method == "turn/start" {
					turns++
					var params struct{ Input []struct{ Text string } }
					if err := json.Unmarshal(event.Params, &params); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(params.Input[0].Text, `"Reference":"demo-`) || !strings.Contains(params.Input[0].Text, "Do not call next or start-next") {
						t.Fatalf("unbound prompt: %s", params.Input[0].Text)
					}
				}
			}
			if turns != test.completed {
				t.Fatalf("turn calls %d", turns)
			}
		})
	}
}

func TestSchedulerPrefixIsStableForNewConversationAndAbsentOnResume(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_failed")
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "needs_attention" || first.RunID == 0 {
		t.Fatalf("first attempt = %+v", first)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
	if err != nil || run.PromptPrefix.TemplateVersion == "" || !strings.HasPrefix(run.PromptPrefix.Text, "PELLETS CODEX TASK PRELOAD v1\n") || run.CachedInputTokens == nil || *run.CachedInputTokens != 7 {
		t.Fatalf("durable prefix = %#v, %v", run.PromptPrefix, err)
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, ".agents", "skills", "pellets", "SKILL.md"), []byte("---\nname: pellets\n---\nChanged before resume.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Prepare the resumed attempt against changed skill and tool-version
	// sources. Its durable conversation provenance must nevertheless remain A.
	t.Setenv("PELLETS_SUPERVISOR_PEER_VERSION", "changed-before-resume")
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	pellet, previous := run.PelletNumber, run.ID
	request.ResumePellet, request.ResumeFrom = &pellet, &previous
	second := awaitSchedule(t, startSchedule(t, s, request))
	if second.State != "completed" {
		t.Fatalf("resume = %+v", second)
	}
	resumed, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, second.RunID)
	if err != nil || resumed.CachedInputTokens == nil || *resumed.CachedInputTokens != 7 || resumed.PromptPrefix != run.PromptPrefix {
		t.Fatalf("resume provenance/telemetry = %#v, %#v, %v", resumed.PromptPrefix, resumed.CachedInputTokens, err)
	}
	var prompts []string
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method != "turn/start" {
			continue
		}
		var params struct{ Input []struct{ Text string } }
		if err := json.Unmarshal(event.Params, &params); err != nil {
			t.Fatal(err)
		}
		prompts = append(prompts, params.Input[0].Text)
	}
	if len(prompts) != 2 || !strings.HasPrefix(prompts[0], run.PromptPrefix.Text) || strings.Contains(prompts[1], "PELLETS CODEX TASK PRELOAD v1") {
		t.Fatalf("new/resume prompt bytes = %#v", prompts)
	}
	if dynamic := strings.Index(prompts[0], "The foreground Pellets server"); dynamic <= strings.Index(prompts[0], "STABLE PELLETS WORKFLOW") || !strings.Contains(prompts[0], "\n{\"Reference\":\""+run.ProjectCode+"-"+fmt.Sprint(run.PelletNumber)+"\"") {
		t.Fatalf("dynamic task did not follow stable prefix: %q", prompts[0])
	}
}

func TestSchedulerPersistsCachedTokensBeforeInputAttention(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_input_live")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, true)
	if run.CachedInputTokens != nil {
		// The live peer requests input before emitting terminal token telemetry.
		t.Fatalf("unexpected pre-completion token telemetry: %#v", run.CachedInputTokens)
	}
	if run.Summary != "Codex is awaiting explicit input." || run.Interaction == nil {
		t.Fatalf("input telemetry/activity = %#v %q", run.CachedInputTokens, run.Summary)
	}
	h.StopNow()
	_ = awaitSchedule(t, h)
}

func TestSchedulerCorrelatesDurableUserInputAndRejectsStaleRevision(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_input_live")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, true)
	if run.State != "awaiting_input" || run.Interaction.Method != "item/tool/requestUserInput" || len(run.Interaction.Questions) != 2 || run.Interaction.RequestID != "41" {
		t.Fatalf("pending interaction: %#v", run)
	}
	_, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision - 1, RequestID: run.Interaction.RequestID, Action: "answer", Answers: map[string][]string{"choice": {"Focused"}, "note": {"Keep it small"}}})
	if err == nil || domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, RequestID: run.Interaction.RequestID, Action: "answer", Answers: map[string][]string{"choice": {"Focused"}, "note": {"Keep it small"}}}); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, h)
	if status.State != "completed" {
		t.Fatalf("interaction completion: %+v", status)
	}
	var response string
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "response" {
			response = string(event.Params)
		}
	}
	if !strings.Contains(response, `"id":41`) || !strings.Contains(response, `"Focused"`) || !strings.Contains(response, `"Keep it small"`) {
		t.Fatalf("exact response not delivered: %s", response)
	}
	if _, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, RequestID: "41", Action: "answer"}); err == nil {
		t.Fatal("duplicate response was accepted")
	}
}

func TestExplicitResumeReissuesInteractionOnNewRunAndRejectsDeadRequest(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_input_live")
	firstHandle := startSchedule(t, s, request)
	first := awaitActiveRun(t, s, true)
	firstHandle.StopNow()
	_ = awaitSchedule(t, firstHandle)
	stopped, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, first.ID)
	if err != nil || stopped.State != "interrupted" || stopped.Interaction != nil {
		t.Fatalf("stopped interaction: %#v %v", stopped, err)
	}
	if _, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: first.ID, Revision: first.Revision, RequestID: first.Interaction.RequestID, Action: "answer"}); err == nil || domain.PublicError(err).Code != "run_process_unavailable" {
		t.Fatalf("dead request accepted: %v", err)
	}
	pellet, previous := first.PelletNumber, first.ID
	request.ResumePellet, request.ResumeFrom = &pellet, &previous
	secondHandle := startSchedule(t, s, request)
	second := awaitActiveRun(t, s, true)
	if second.ID == first.ID || second.ResumeFrom == nil || *second.ResumeFrom != first.ID || second.Interaction == nil {
		t.Fatalf("reissued interaction: %#v", second)
	}
	if _, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: second.ID, Revision: second.Revision, RequestID: second.Interaction.RequestID, Action: "answer", Answers: map[string][]string{"choice": {"Focused"}, "note": {"Resume safely"}}}); err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, secondHandle); status.State != "completed" {
		t.Fatalf("resumed interaction completion: %+v", status)
	}
}

func TestSchedulerRoutesOneTimeApprovalDenialWithoutBroaderPermission(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_approval_live")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, true)
	if run.Interaction.RequestID != `"approval-7"` || !strings.Contains(run.Interaction.Title, "one command") {
		t.Fatalf("approval interaction: %#v", run.Interaction)
	}
	if _, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, RequestID: run.Interaction.RequestID, Action: "decline"}); err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, h); status.State != "completed" {
		t.Fatalf("denial continuation: %+v", status)
	}
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "response" {
			payload := string(event.Params)
			if !strings.Contains(payload, `"decision":"decline"`) || strings.Contains(payload, "acceptForSession") || strings.Contains(payload, "Amendment") {
				t.Fatalf("unsafe approval response: %s", payload)
			}
		}
	}
}

func TestSchedulerSteersFollowUpIntoExactActiveTurn(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_followup_live")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, false)
	if _, err := s.options.Supervisor.SubmitInteraction(context.Background(), s.options.Database, InteractionSubmission{RunID: run.ID, Revision: run.Revision, FollowUp: "Focus on the failing test first."}); err != nil {
		t.Fatal(err)
	}
	if status := awaitSchedule(t, h); status.State != "completed" {
		t.Fatalf("follow-up completion: %+v", status)
	}
	var steer map[string]any
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/steer" {
			if err := json.Unmarshal(event.Params, &steer); err != nil {
				t.Fatal(err)
			}
		}
	}
	if steer["threadId"] != "thread" || steer["expectedTurnId"] != "turn" || !strings.Contains(fmt.Sprint(steer["input"]), "failing test") {
		t.Fatalf("steer params: %#v", steer)
	}
}

func TestSchedulerPreservesApprovalReviewAttention(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_approval_live")
	h := startSchedule(t, s, request)
	run := awaitActiveRun(t, s, true)
	if run.State != "awaiting_input" || run.Interaction == nil || run.Interaction.Method != "item/commandExecution/requestApproval" || run.Summary != "Automatic review routed this approval for an explicit human decision." {
		t.Fatalf("approval activity = %#v", run)
	}
	h.StopNow()
	_ = awaitSchedule(t, h)
}

func TestWebResumeRestoresCapturedFiltersInsteadOfBrowserValues(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, queue := schedulerFixture(t, executable, "schedule_failed")
	originalGroup, originalExternal := "original group", "original external"
	const pelletNumber int64 = 1
	reference := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: pelletNumber}
	if _, err := queue.UpdatePellet(context.Background(), request.Selected, reference, storage.PelletChanges{
		Group: storage.NullableTextChange{Set: true, Value: &originalGroup}, ExternalID: storage.NullableTextChange{Set: true, Value: &originalExternal},
	}); err != nil {
		t.Fatal(err)
	}
	request.Group, request.ExternalID = &originalGroup, &originalExternal
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.RunID == 0 {
		t.Fatalf("first run missing: %+v", first)
	}
	changedGroup, changedExternal := "changed group", "changed external"
	if _, err := queue.UpdatePellet(context.Background(), request.Selected, reference, storage.PelletChanges{
		Group: storage.NullableTextChange{Set: true, Value: &changedGroup}, ExternalID: storage.NullableTextChange{Set: true, Value: &changedExternal},
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := sqlite.OpenWebReader(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := sqlite.OpenWebWriter(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	application := &WebApplication{Reader: reader, Writer: writer, Executions: s.options.Supervisor, Scheduler: s, Database: s.options.Database}
	maliciousGroup, maliciousExternal := "browser group", "browser external"
	pellet, previous := pelletNumber, first.RunID
	handle, err := application.StartSchedule(context.Background(), request.Selected.Project, request.Selected.Workspace.ID, ScheduleRequest{Mode: "run_one", ResumePellet: &pellet, ResumeFrom: &previous, Group: &maliciousGroup, ExternalID: &maliciousExternal})
	if err != nil {
		t.Fatal(err)
	}
	status := handle.Status()
	if status.Group == nil || status.ExternalID == nil || *status.Group != originalGroup || *status.ExternalID != originalExternal {
		t.Fatalf("resume filters were not restored: %+v", status)
	}
	_ = awaitSchedule(t, handle)
}

func TestSchedulerUnidentifiedThreadCannotBeRetriedWithFreshPrefix(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_prethread_failure")
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "needs_attention" {
		t.Fatalf("pre-thread failure = %+v", first)
	}
	previous, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, first.RunID)
	if err != nil || previous.ThreadID != "" {
		t.Fatalf("pre-thread evidence = %#v, %v", previous, err)
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, ".agents", "skills", "pellets", "SKILL.md"), []byte("---\nname: pellets\n---\nFresh retry skill.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PELLETS_SUPERVISOR_PEER_VERSION", "fresh-prethread-retry")
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	pellet, resumeFrom := previous.PelletNumber, previous.ID
	request.ResumePellet, request.ResumeFrom = &pellet, &resumeFrom
	second := awaitSchedule(t, startSchedule(t, s, request))
	if second.State != "needs_attention" || second.Reason != "resume_conversation_unidentified" || second.Started != 0 {
		t.Fatalf("uncertain thread creation was retried = %+v", second)
	}
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			t.Fatal("unidentified thread generated work")
		}
	}
}

func TestSchedulerNeverAdvancesOnTurnOrClosedAlone(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"schedule_turn_only", "schedule_close_only", "schedule_commit_only", "schedule_failed"} {
		t.Run(mode, func(t *testing.T) {
			s, r, q := schedulerFixture(t, executable, mode)
			r.Mode = "drain"
			second, err := q.CreatePellet(context.Background(), r.Selected, storage.NewPellet{Title: "untouched"})
			if err != nil {
				t.Fatal(err)
			}
			status := awaitSchedule(t, startSchedule(t, s, r))
			if status.State != "needs_attention" || status.Completed != 0 || status.Started != 1 {
				t.Fatalf("unsafe advance: %+v", status)
			}
			pellet, err := q.ReadPellet(context.Background(), r.Selected, second.Reference)
			if err != nil || pellet.Status != domain.PelletOpen {
				t.Fatalf("second changed: %+v %v", pellet, err)
			}
		})
	}
}

func TestSchedulerWatchIdleRecoveryReadinessAndImmutableFilters(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, q := schedulerFixture(t, executable, "schedule_success")
	r.Mode = "watch"
	r.Limit = 1
	group, external := " exact group ", "exact ID"
	r.Group, r.ExternalID = &group, &external
	var ready atomic.Bool
	s.options.Ready = func(context.Context, storage.ResolvedProject, storage.Pellet) (bool, error) { return ready.Load(), nil }
	s.options.RecoveryInterval = 20 * time.Millisecond
	ctx := context.Background()
	h := startSchedule(t, s, r)
	group, external = "mutated", "changed"
	awaitScheduleState(t, h, "waiting")
	if _, err := os.Stat(filepath.Join(s.options.Database.Root, "fake-events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("idle launched Codex: %v", err)
	}
	if _, err := s.Start(ctx, r); err == nil || domain.PublicError(err).Code != "workspace_schedule_busy" {
		t.Fatalf("duplicate schedule: %v", err)
	}
	exactGroup, exactID := " exact group ", "exact ID"
	p, err := q.CreatePellet(ctx, r.Selected, storage.NewPellet{Title: "filtered", Group: &exactGroup, ExternalID: &exactID})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	read, err := q.ReadPellet(ctx, r.Selected, p.Reference)
	if err != nil || read.Status != domain.PelletOpen {
		t.Fatalf("unready changed: %+v %v", read, err)
	}
	if _, err := os.Stat(filepath.Join(s.options.Database.Root, "fake-events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("unready launched Codex")
	}
	ready.Store(true)
	status := awaitSchedule(t, h)
	if status.Completed != 1 || status.PelletNumber != p.Reference.Number || *status.Group != exactGroup || *status.ExternalID != exactID {
		t.Fatalf("capture/wakeup: %+v", status)
	}
	stored, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, status.RunID)
	if err != nil || *stored.Group != exactGroup || *stored.ExternalID != exactID {
		t.Fatalf("durable filters: %+v %v", stored, err)
	}
}

func TestSchedulerWatchDatabaseMonitorWakeup(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, q := schedulerFixture(t, executable, "schedule_success")
	r.Mode = "watch"
	r.Limit = 1
	group := "wake"
	r.Group = &group
	monitor, err := sqlite.OpenDataVersionMonitor(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	changes := make(chan struct{}, 1)
	monitorCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baseline, err := monitor.DataVersion(monitorCtx)
	if err != nil {
		t.Fatal(err)
	}
	s.options.Subscribe = func() (<-chan struct{}, func()) { return changes, func() {} }
	s.options.RecoveryInterval = time.Hour
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
				version, err := monitor.DataVersion(monitorCtx)
				if err == nil && version != baseline {
					baseline = version
					select {
					case changes <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	h := startSchedule(t, s, r)
	awaitScheduleState(t, h, "waiting")
	if _, err := q.CreatePellet(context.Background(), r.Selected, storage.NewPellet{Title: "wake target", Group: &group}); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, h)
	if status.Completed != 1 {
		t.Fatalf("monitor wake: %+v", status)
	}
}

func TestSchedulerStopsAndQueueMutation(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, action := range []string{"after", "now", "shutdown", "mutation"} {
		t.Run(action, func(t *testing.T) {
			s, r, q := schedulerFixture(t, executable, "schedule_gate")
			r.Mode = "drain"
			ctx := context.Background()
			second, err := q.CreatePellet(ctx, r.Selected, storage.NewPellet{Title: "second"})
			if err != nil {
				t.Fatal(err)
			}
			h := startSchedule(t, s, r)
			awaitScheduledTurn(t, s)
			switch action {
			case "after":
				h.StopAfter()
			case "now":
				h.StopNow()
			case "shutdown":
				s.Close()
			case "mutation":
				if _, err := q.TransitionPellet(ctx, r.Selected, second.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletDefer}); err != nil {
					t.Fatal(err)
				}
				if _, err := q.CreatePellet(ctx, r.Selected, storage.NewPellet{Title: "new third"}); err != nil {
					t.Fatal(err)
				}
			}
			if action != "now" && action != "shutdown" {
				if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-complete"), []byte("yes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			status := awaitSchedule(t, h)
			want := 1
			if action == "now" || action == "shutdown" {
				want = 0
			}
			if action == "mutation" {
				want = 2
			}
			if status.Completed != want {
				t.Fatalf("stop result: %+v", status)
			}
			if action == "after" && status.Reason != "stop_after_pellet" || action == "now" && status.State != "stopped" || action == "mutation" && status.PelletNumber != 3 {
				t.Fatalf("wrong result: %+v", status)
			}
		})
	}
}

func TestSchedulerRevalidatesFiltersAfterPreflightAndStopsWithoutRetry(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, q := schedulerFixture(t, executable, "schedule_success")
	group := "exact"
	r.Group = &group
	ref := domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1}
	if _, err := q.UpdatePellet(context.Background(), r.Selected, ref, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &group}}); err != nil {
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
	h := startSchedule(t, s, r)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("preflight did not begin")
	}
	changed := "other"
	if _, err := q.UpdatePellet(context.Background(), r.Selected, ref, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &changed}}); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	status := awaitSchedule(t, h)
	if status.State != "needs_attention" || status.Started != 1 || status.Completed != 0 {
		t.Fatalf("filter race: %+v", status)
	}
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			t.Fatal("started turn after filter changed")
		}
	}
}

func TestSchedulerResumeRevalidatesGenerationAfterPreflight(t *testing.T) {
	ctx := context.Background()
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_input_live")
	firstHandle := startSchedule(t, s, request)
	first := awaitActiveRun(t, s, true)
	firstHandle.StopNow()
	awaitSchedule(t, firstHandle)
	stopped, err := s.options.Supervisor.ReadRun(ctx, s.options.Database, first.ID)
	if err != nil || stopped.State != "interrupted" {
		t.Fatalf("original interrupted run: %#v %v", stopped, err)
	}
	if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
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
	request.ResumePellet, request.ResumeFrom = &first.PelletNumber, &first.ID
	h := startSchedule(t, s, request)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("resume preflight did not begin")
	}
	ref := domain.PelletReference{ProjectCode: first.ProjectCode, Number: first.PelletNumber}
	for _, operation := range []storage.PelletLifecycleOperation{storage.PelletRelease, storage.PelletStart} {
		if _, err := q.TransitionPellet(ctx, request.Selected, ref, storage.PelletLifecycleRequest{Operation: operation}); err != nil {
			t.Fatal(err)
		}
	}
	close(proceed)
	status := awaitSchedule(t, h)
	if status.State != "needs_attention" || status.Reason != "execution_run_conflict" || status.Completed != 0 {
		t.Fatalf("stale resume result: %+v", status)
	}
	runs, err := s.options.Supervisor.ListWorkspaceRuns(ctx, s.options.Database, request.Selected.Workspace.ID, 8)
	if err != nil || len(runs) != 1 || runs[0].ID != stopped.ID || runs[0].Revision != stopped.Revision || runs[0].State != "interrupted" {
		t.Fatalf("stale resume changed durable history: %#v %v", runs, err)
	}
	current, err := q.ReadPellet(ctx, request.Selected, ref)
	if err != nil || current.Status != domain.PelletInProgress || current.Workspace == nil || current.Workspace.ID != request.Selected.Workspace.ID || current.ImplementationRevision != first.ImplementationRevision+1 {
		t.Fatalf("new generation ownership changed: %#v %v", current, err)
	}
	starts := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "thread/resume" {
			t.Fatal("resumed old conversation after lifecycle generation changed")
		}
		if event.Method == "turn/start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("stale resume started extra turns: %d", starts)
	}
	// The new generation still has no run. Explicit fresh recovery remains
	// possible without continuing the interrupted generation's conversation.
	s.options.Supervisor.options.Prepare = prepare
	request.ResumeFrom = nil
	if fresh := awaitSchedule(t, startSchedule(t, s, request)); fresh.Completed != 1 {
		t.Fatalf("fresh recovery after rejected continuation: %+v", fresh)
	}
	runs, err = s.options.Supervisor.ListWorkspaceRuns(ctx, s.options.Database, request.Selected.Workspace.ID, 8)
	if err != nil || len(runs) != 2 || runs[0].ResumeFrom != nil || runs[0].ImplementationRevision != current.ImplementationRevision {
		t.Fatalf("fresh recovery retained old generation: %#v %v", runs, err)
	}
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "thread/resume" {
			t.Fatal("fresh recovery resumed old conversation")
		}
	}
}

func TestSchedulerCompetingServerCannotSelectWhileExecutionOwned(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, _ := schedulerFixture(t, executable, "schedule_gate")
	h := startSchedule(t, s, r)
	awaitScheduledTurn(t, s)
	other := NewScheduler(context.Background(), s.options)
	defer other.Close()
	status := awaitSchedule(t, startSchedule(t, other, r))
	if status.Reason != "workspace_execution_busy" || status.Started != 0 {
		t.Fatalf("competing scheduler: %+v", status)
	}
	h.StopNow()
	awaitSchedule(t, h)
}

func TestSchedulerResumeUsesExactRecordedThread(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	ctx := context.Background()
	previous, err := supervisor.options.Recorder.Begin(ctx, request.Database, request.Selected, request.Capture)
	if err != nil {
		t.Fatal(err)
	}
	previous, err = supervisor.options.Recorder.Save(ctx, request.Database, storage.UpdateExecutionRun{ID: previous.ID, ExpectedRevision: previous.Revision, Progress: storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "unknown", ThreadID: "thread", TurnID: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(ctx, SchedulerOptions{Database: request.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}})
	defer s.Close()
	status := awaitSchedule(t, startSchedule(t, s, ScheduleRequest{Selected: request.Selected, Mode: "run_one", ResumePellet: &previous.PelletNumber, ResumeFrom: &previous.ID}))
	if status.Completed != 1 {
		t.Fatalf("resume completion: %+v", status)
	}
	var resumes int
	for _, event := range readPeerEvents(t, request.Database.Root) {
		if event.Method == "thread/start" {
			t.Fatal("resumed attempt started another thread")
		}
		if event.Method == "thread/resume" {
			resumes++
			var params struct{ ThreadID string }
			if err := json.Unmarshal(event.Params, &params); err != nil || params.ThreadID != "thread" {
				t.Fatalf("resume params: %s %v", event.Params, err)
			}
		}
	}
	if resumes != 1 {
		t.Fatalf("resume calls: %d", resumes)
	}
}

func TestSchedulerExplicitResumeAndIdleStop(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, q := schedulerFixture(t, executable, "schedule_success")
	ctx := context.Background()
	ref := domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1}
	if _, err := q.TransitionPellet(ctx, r.Selected, ref, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, startSchedule(t, s, r))
	if status.Reason != "schedule_resume_required" || status.Started != 0 {
		t.Fatalf("implicit resume: %+v", status)
	}
	r.ResumePellet = &ref.Number
	status = awaitSchedule(t, startSchedule(t, s, r))
	if status.Completed != 1 {
		t.Fatalf("explicit resume: %+v", status)
	}
	r.ResumePellet = nil
	r.Mode = "watch"
	h := startSchedule(t, s, r)
	awaitScheduleState(t, h, "waiting")
	h.StopAfter()
	status = awaitSchedule(t, h)
	if status.State != "stopped" || status.Started != 0 {
		t.Fatalf("idle stop: %+v", status)
	}
}
