package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func schedulerContextGroup(t *testing.T, s *Scheduler, request ScheduleRequest, q *sqlite.PelletRepository) (*sqlite.GroupRepository, storage.Group) {
	t.Helper()
	ctx := context.Background()
	groups, err := sqlite.OpenGroupRepository(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { groups.Close() })
	g, err := groups.CreateGroupWithContext(ctx, request.Selected.Project, "actual group", "# Captured original\r\n```mermaid\nflowchart LR\n A --> B\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	if _, err = q.UpdatePellet(ctx, request.Selected, ref, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &g.Name}}); err != nil {
		t.Fatal(err)
	}
	return groups, g
}

func implementationPrompts(t *testing.T, s *Scheduler) []string {
	t.Helper()
	var prompts []string
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method != "turn/start" {
			continue
		}
		var params struct {
			ThreadID string
			Input    []struct{ Text string }
		}
		if err := json.Unmarshal(event.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.ThreadID == "thread" {
			prompts = append(prompts, params.Input[0].Text)
		}
	}
	return prompts
}

func TestSchedulerGroupContextCapturedPerDrainAndWatchPellet(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"drain", "watch"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s, request, q := schedulerFixture(t, executable, "schedule_gate")
			request.Mode, request.Limit = mode, 2
			groups, g := schedulerContextGroup(t, s, request, q)
			if _, err := q.CreatePellet(ctx, request.Selected, storage.NewPellet{Title: "next task", Group: &g.Name}); err != nil {
				t.Fatal(err)
			}
			h := startSchedule(t, s, request)
			awaitScheduledTurn(t, s)
			before := awaitActiveRun(t, s, false)
			if before.Group != nil || before.GroupContext.Group == nil || before.GroupContext.Group.ID != g.ID {
				t.Fatalf("unfiltered capture: %+v", before.GroupContext)
			}
			updated, err := groups.EditGroupContext(ctx, request.Selected.Project, g.ID, g.Revision, "# Updated for the NEXT pellet\n")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-complete"), []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			status := awaitSchedule(t, h)
			if status.State != "stopped" || status.Reason != "limit_reached" || status.Completed != 2 {
				t.Fatalf("schedule: %+v", status)
			}
			runs, err := s.options.Supervisor.ListWorkspaceRuns(ctx, s.options.Database, request.Selected.Workspace.ID, 8)
			if err != nil || len(runs) != 2 {
				t.Fatalf("runs: %d %v", len(runs), err)
			}
			if !reflect.DeepEqual(runs[1].GroupContext, before.GroupContext) || runs[0].GroupContext.Group.Context != updated.Context || runs[0].GroupContext.Group.Revision != updated.Revision {
				t.Fatal("schedule froze or refreshed the wrong context")
			}
			prompts := implementationPrompts(t, s)
			if len(prompts) != 2 {
				t.Fatalf("prompts: %d", len(prompts))
			}
			for i, doc := range []string{g.Context, updated.Context} {
				if !strings.HasPrefix(prompts[i], runs[1-i].PromptPrefix.Text+"CAPTURED GROUP CONTEXT v1\n") || !strings.Contains(prompts[i], "\n"+doc+"\nEND GROUP MARKDOWN") || strings.Index(prompts[i], "END GROUP MARKDOWN") >= strings.Index(prompts[i], "The foreground Pellets server") {
					t.Fatalf("prompt %d did not receive exact ordered snapshot", i)
				}
			}
		})
	}
}

func TestSchedulerGroupContextResumeAndFreshConversationRetainSnapshot(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "fresh"}[fresh], func(t *testing.T) {
			ctx := context.Background()
			s, request, q := schedulerFixture(t, executable, "schedule_failed")
			groups, g := schedulerContextGroup(t, s, request, q)
			first := awaitSchedule(t, startSchedule(t, s, request))
			if first.State != "needs_attention" {
				t.Fatalf("first: %+v", first)
			}
			run, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, first.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := groups.EditGroupContext(ctx, request.Selected.Project, g.ID, g.Revision, ""); err != nil {
				t.Fatal(err)
			}
			request.ResumeFrom, request.ResumePellet, request.FreshConversation = &run.ID, &run.PelletNumber, fresh
			if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
				t.Fatal(err)
			}
			second := awaitSchedule(t, startSchedule(t, s, request))
			if second.State != "completed" {
				t.Fatalf("resumed: %+v", second)
			}
			resumed, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, second.RunID)
			if err != nil || !reflect.DeepEqual(resumed.GroupContext, run.GroupContext) {
				t.Fatalf("changed snapshot: %+v %v", resumed.GroupContext, err)
			}
			prompts := implementationPrompts(t, s)
			if len(prompts) != 2 {
				t.Fatalf("prompts=%d", len(prompts))
			}
			if strings.Contains(prompts[1], "CAPTURED GROUP CONTEXT") != fresh || strings.Contains(prompts[1], g.Context) != fresh {
				t.Fatal("Resume duplicated context or fresh recovery lost original context")
			}
		})
	}
}

func TestSchedulerGroupContextRetainedAfterLiveRenameAndContinuation(t *testing.T) {
	ctx := context.Background()
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_change_live")
	groups, g := schedulerContextGroup(t, s, request, q)
	h := startSchedule(t, s, request)
	awaitScheduledTurn(t, s)
	before := awaitActiveRun(t, s, false)
	g, err := groups.RenameGroup(ctx, request.Selected.Project, g.ID, g.Revision, "renamed during turn")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = groups.EditGroupContext(ctx, request.Selected.Project, g.ID, g.Revision, "cleared and replaced"); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, h)
	if status.State != "completed" {
		t.Fatalf("live rename: %+v", status)
	}
	after, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, status.RunID)
	if err != nil || !reflect.DeepEqual(after.GroupContext, before.GroupContext) {
		t.Fatalf("live rename changed snapshot: %+v %v", after.GroupContext, err)
	}
	prompts := implementationPrompts(t, s)
	if len(prompts) != 2 || strings.Contains(prompts[1], "CAPTURED GROUP CONTEXT") || strings.Contains(prompts[1], "cleared and replaced") {
		t.Fatal("continuation fetched or duplicated group context")
	}
}

func TestSchedulerFinalizationRecoveryKeepsGroupContextWithoutTurn(t *testing.T) {
	ctx := context.Background()
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_success")
	groups, g := schedulerContextGroup(t, s, request, q)
	open := s.options.Supervisor.options.Recorder.Open
	var failed atomic.Bool
	s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		db, err := open(ctx, path)
		if err != nil {
			return nil, err
		}
		return finalizationFailureDatabase{ExecutionRunDatabase: db, fail: func(update storage.UpdateExecutionRun) bool {
			return update.Progress.Phase == "commit" && failed.CompareAndSwap(false, true)
		}}, nil
	}
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "needs_attention" || !failed.Load() {
		t.Fatalf("first: %+v", first)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, first.RunID)
	if err != nil || run.Finalization == nil {
		t.Fatalf("finalization evidence: %v", err)
	}
	if _, err = groups.EditGroupContext(ctx, request.Selected.Project, g.ID, g.Revision, ""); err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &run.ID, &run.PelletNumber
	second := awaitSchedule(t, startSchedule(t, s, request))
	if second.State != "completed" {
		t.Fatalf("recovery: %+v", second)
	}
	after, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, second.RunID)
	if err != nil || !reflect.DeepEqual(after.GroupContext, run.GroupContext) || len(implementationPrompts(t, s)) != 1 {
		t.Fatalf("finalization recaptured context or repeated implementation: %v", err)
	}
}

func TestSchedulerFirstTurnAfterThreadOnlyRecoveryReceivesCapturedContext(t *testing.T) {
	ctx := context.Background()
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_empty_history")
	groups, g := schedulerContextGroup(t, s, request, q)
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	if _, err := q.TransitionPellet(ctx, request.Selected, ref, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	recorder := s.options.Supervisor.options.Recorder
	// The thread exists durably, but no turn was sent before interruption.
	capture := storage.RunCapture{ProjectID: request.Selected.Project.ID, WorkspaceID: request.Selected.Workspace.ID, PelletNumber: 1, Mode: "run_one",
		Settings:     storage.EffectiveRunSettings{Codex: storage.CodexRunSettings{Executable: "codex", Limits: storage.CodexRunLimits{MaxMessageBytes: 8 << 20, EventBuffer: 8, MaxPending: 8, StderrBytes: 1024}}, ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"},
		PromptPrefix: storage.PromptPrefix{TemplateVersion: "test-prefix", SkillSHA256: strings.Repeat("a", 64), HelpSHA256: strings.Repeat("b", 64), ToolExecutable: "pl", ToolVersion: "test", Text: "stable\n"}}
	run, err := recorder.Begin(ctx, s.options.Database, request.Selected, capture)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recorder.Save(ctx, s.options.Database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: storage.RunProgress{Phase: "thread_start", State: "interrupted", Outcome: "unknown", ThreadID: "thread"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := groups.EditGroupContext(ctx, request.Selected.Project, g.ID, g.Revision, "changed after thread start"); err != nil {
		t.Fatal(err)
	}
	request.ResumeFrom, request.ResumePellet = &run.ID, &run.PelletNumber
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "completed" {
		t.Fatalf("resume: %+v", status)
	}
	prompts := implementationPrompts(t, s)
	if len(prompts) != 1 || !strings.HasPrefix(prompts[0], run.PromptPrefix.Text+"CAPTURED GROUP CONTEXT v1\n") || !strings.Contains(prompts[0], g.Context) || strings.Contains(prompts[0], "changed after thread start") {
		t.Fatal("first turn of existing empty thread did not receive original context")
	}
}
