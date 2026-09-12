package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestRunningEditsUseTerraAndFinishUpdatedPellet(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"schedule_change_live", "schedule_change_finish_first", "schedule_change_cosmetic"} {
		t.Run(mode, func(t *testing.T) {
			s, request, q := schedulerFixture(t, executable, mode)
			h := startSchedule(t, s, request)
			awaitScheduledTurn(t, s)
			ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
			before, err := q.ReadPellet(context.Background(), request.Selected, ref)
			if err != nil {
				t.Fatal(err)
			}
			description := "New requirement: support keyboard navigation."
			if _, err = q.UpdatePellet(context.Background(), request.Selected, ref, storage.PelletChanges{Description: &description}); err != nil {
				t.Fatal(err)
			}
			status := awaitSchedule(t, h)
			if status.State != "completed" {
				t.Fatalf("updated run failed: %+v", status)
			}
			after, err := q.ReadPellet(context.Background(), request.Selected, ref)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != domain.PelletClosed || after.Description != description || after.ImplementationRevision != before.ImplementationRevision+1 {
				t.Fatalf("wrong completion: %+v", after)
			}
			records, err := os.ReadFile(filepath.Join(s.options.Database.Root, "fake-events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			assessor, steers, implementationTurns := 0, 0, 0
			for _, line := range strings.Split(strings.TrimSpace(string(records)), "\n") {
				var event struct {
					Method string
					Params json.RawMessage
				}
				if json.Unmarshal([]byte(line), &event) != nil {
					continue
				}
				var p struct {
					Model, Effort, Sandbox, ThreadID string
					Config                           map[string]any
					SandboxPolicy                    struct{ Type string }
					Input                            []struct{ Text string }
				}
				if json.Unmarshal(event.Params, &p) != nil {
					continue
				}
				if event.Method == "thread/start" && p.Model == changeAssessorModel {
					assessor++
					if p.Sandbox != "read-only" || p.Config["model_reasoning_effort"] != "max" {
						t.Fatalf("unsafe assessor: %s", event.Params)
					}
				}
				if event.Method == "turn/start" && p.Model == changeAssessorModel {
					if p.Effort != "max" || p.SandboxPolicy.Type != "readOnly" || !strings.Contains(p.Input[0].Text, description) {
						t.Fatalf("wrong assessment request: %s", event.Params)
					}
				}
				if event.Method == "turn/start" && p.ThreadID == "thread" {
					implementationTurns++
				}
				if event.Method == "turn/steer" {
					steers++
				}
			}
			if assessor != 1 {
				t.Fatalf("assessors=%d", assessor)
			}
			if mode == "schedule_change_cosmetic" {
				if steers != 0 || implementationTurns != 1 {
					t.Fatalf("cosmetic edit repeated implementation: %d %d", steers, implementationTurns)
				}
			} else if implementationTurns != 2 || mode == "schedule_change_live" && steers != 1 {
				t.Fatalf("missing updated verification: turns=%d steers=%d", implementationTurns, steers)
			}
		})
	}
}

func TestEditAtFinalizationCaptureReturnsToAssessment(t *testing.T) {
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_change_boundary")
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	var edited atomic.Bool
	open := s.options.Supervisor.options.Recorder.Open
	s.options.Supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		db, err := open(ctx, path)
		if err != nil {
			return nil, err
		}
		return finalizationFailureDatabase{ExecutionRunDatabase: db, fail: func(update storage.UpdateExecutionRun) bool {
			if update.Progress.Phase == "verification" && edited.CompareAndSwap(false, true) {
				description := "A last-moment requirement change"
				if _, err := q.UpdatePellet(ctx, request.Selected, ref, storage.PelletChanges{Description: &description}); err != nil {
					t.Error(err)
				}
			}
			return false
		}}, nil
	}
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "completed" || !edited.Load() {
		t.Fatalf("finalization race: %+v", status)
	}
	assessments := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "thread/start" && strings.Contains(string(event.Params), changeAssessorModel) {
			assessments++
		}
	}
	if assessments != 1 {
		t.Fatalf("last-moment edit assessed %d times", assessments)
	}
}

func TestRapidRunningEditsReachLatestRequirements(t *testing.T) {
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_change_live")
	h := startSchedule(t, s, request)
	awaitScheduledTurn(t, s)
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	for _, description := range []string{"Keyboard support", "Keyboard and touch support"} {
		if _, err := q.UpdatePellet(context.Background(), request.Selected, ref, storage.PelletChanges{Description: &description}); err != nil {
			t.Fatal(err)
		}
	}
	status := awaitSchedule(t, h)
	if status.State != "completed" {
		t.Fatalf("rapid edits: %+v", status)
	}
	runs, err := s.options.Supervisor.ListWorkspaceRuns(context.Background(), s.options.Database, request.Selected.Workspace.ID, 8)
	if err != nil || len(runs) != 1 || runs[0].PelletDescription != "Keyboard and touch support" {
		t.Fatalf("lost edit: %+v %v", runs, err)
	}
	assessors := 0
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "thread/start" && strings.Contains(string(event.Params), changeAssessorModel) {
			assessors++
		}
	}
	if assessors != 2 {
		t.Fatalf("assessed %d of 2 edits", assessors)
	}
}

func TestStopDuringChangeAssessmentPreservesPendingEdit(t *testing.T) {
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_change_wait")
	h := startSchedule(t, s, request)
	awaitScheduledTurn(t, s)
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	description := "Updated requirements"
	if _, err := q.UpdatePellet(context.Background(), request.Selected, ref, storage.PelletChanges{Description: &description}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		found := false
		for _, event := range readPeerEvents(t, s.options.Database.Root) {
			if event.Method == "turn/start" && strings.Contains(string(event.Params), changeAssessorModel) {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("assessment did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.StopNow()
	status := awaitSchedule(t, h)
	if status.State != "stopped" || status.Completed != 0 {
		t.Fatalf("stop failed: %+v", status)
	}
	p, err := q.ReadPellet(context.Background(), request.Selected, ref)
	if err != nil || p.Status != domain.PelletInProgress || p.Description != description {
		t.Fatalf("stop lost edit: %+v %v", p, err)
	}
}

func TestInvalidChangeAssessmentCannotFinalize(t *testing.T) {
	s, request, q := schedulerFixture(t, installSupervisorPeer(t), "schedule_change_invalid")
	h := startSchedule(t, s, request)
	awaitScheduledTurn(t, s)
	ref := domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: 1}
	description := "Changed behavior"
	if _, err := q.UpdatePellet(context.Background(), request.Selected, ref, storage.PelletChanges{Description: &description}); err != nil {
		t.Fatal(err)
	}
	status := awaitSchedule(t, h)
	if status.State != "needs_attention" || status.Reason != "change_assessment_invalid" {
		t.Fatalf("invalid assessment: %+v", status)
	}
	p, err := q.ReadPellet(context.Background(), request.Selected, ref)
	if err != nil || p.Status != domain.PelletInProgress || p.Description != description {
		t.Fatalf("edit lost or falsely closed: %+v %v", p, err)
	}
}
