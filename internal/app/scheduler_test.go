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
	s, request, _ := schedulerFixture(t, executable, "schedule_input")
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "needs_attention" || status.Reason != "codex_input_required" {
		t.Fatalf("input attention = %+v", status)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, status.RunID)
	if err != nil || run.CachedInputTokens == nil || *run.CachedInputTokens != 7 {
		t.Fatalf("input telemetry = %#v, %v", run.CachedInputTokens, err)
	}
}

func TestSchedulerPreThreadRetryUsesFreshChangedPrefix(t *testing.T) {
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
	if second.State != "completed" {
		t.Fatalf("pre-thread retry = %+v", second)
	}
	retried, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, second.RunID)
	if err != nil || retried.PromptPrefix == previous.PromptPrefix || !strings.Contains(retried.PromptPrefix.Text, "Fresh retry skill.\n") {
		t.Fatalf("fresh retry provenance = %#v, %v", retried.PromptPrefix, err)
	}
	var prompt string
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "turn/start" {
			var params struct{ Input []struct{ Text string } }
			if err := json.Unmarshal(event.Params, &params); err != nil {
				t.Fatal(err)
			}
			prompt = params.Input[0].Text
		}
	}
	if !strings.HasPrefix(prompt, retried.PromptPrefix.Text) {
		t.Fatalf("fresh prefix absent from new conversation: %q", prompt)
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
	previous, err = supervisor.options.Recorder.Save(ctx, request.Database, storage.UpdateExecutionRun{ID: previous.ID, ExpectedRevision: previous.Revision, Progress: storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "unknown", ThreadID: "thread"}})
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
