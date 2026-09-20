package app

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func preparedContextReviewCheckpoint(t *testing.T, executable, mode string, sameGroup bool) (*Scheduler, ScheduleRequest, storage.Pellet, []storage.ExecutionRun, *sqlite.GroupRepository, storage.Group) {
	t.Helper()
	ctx := context.Background()
	var groups *sqlite.GroupRepository
	var group storage.Group
	s, request, checkpoint, runs := preparedReviewCheckpointWithSetup(t, executable, mode, func(s *Scheduler, request ScheduleRequest, q *sqlite.PelletRepository, i int) {
		var err error
		if i == 0 {
			groups, err = sqlite.OpenGroupRepository(ctx, s.options.Database.Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { groups.Close() })
			group, err = groups.CreateGroupWithContext(ctx, request.Selected.Project, "original group", "# Historical first Ω\r\n```mermaid\nflowchart LR\n A --> B\n```\n")
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 {
			if sameGroup {
				group, err = groups.EditGroupContext(ctx, request.Selected.Project, group.ID, group.Revision, "# Historical second")
			} else {
				group, err = groups.CreateGroupWithContext(ctx, request.Selected.Project, "another group", "# Historical second")
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		name := group.Name
		if i == 1 {
			name = "unselected group"
		}
		_, err = q.UpdatePellet(ctx, request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: int64(i + 1)}, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &name}})
		if err != nil {
			t.Fatal(err)
		}
	})
	// Current shared context and the checkpoint's own group must not become
	// requirements for either implementation, despite unfiltered scheduling.
	var err error
	group, err = groups.EditGroupContext(ctx, request.Selected.Project, group.ID, group.Revision, "LIVE CONTEXT MUST NOT APPEAR")
	if err != nil {
		t.Fatal(err)
	}
	own, err := groups.CreateGroupWithContext(ctx, request.Selected.Project, "checkpoint group", "CHECKPOINT CONTEXT MUST NOT APPEAR")
	if err != nil {
		t.Fatal(err)
	}
	q, err := sqlite.OpenPelletRepository(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	external := "exact:checkpoint"
	checkpoint, err = q.UpdatePellet(ctx, request.Selected, checkpoint.Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &own.Name}, ExternalID: storage.NullableTextChange{Set: true, Value: &external}})
	if err != nil {
		t.Fatal(err)
	}
	return s, request, checkpoint, runs, groups, group
}

func assertReviewContexts(t *testing.T, snapshot *storage.ReviewSnapshot, runs []storage.ExecutionRun) {
	t.Helper()
	if snapshot == nil || snapshot.Version != 2 || len(snapshot.Targets) != 2 || len(snapshot.GroupContexts) != 2 {
		t.Fatalf("inexact review context scope: %+v", snapshot)
	}
	for i, index := range []int{0, 2} {
		target, got, want := snapshot.Targets[i], snapshot.GroupContexts[i], runs[index]
		if got.ProjectID != target.ProjectID || got.Number != target.Number || got.RunID != want.ID || !reflect.DeepEqual(got.Snapshot, want.GroupContext) {
			t.Fatalf("target %d context: %+v; want %+v", i, got, want.GroupContext)
		}
	}
}

func assertCheckpointContextPrompts(t *testing.T, s *Scheduler, run storage.ExecutionRun, runs []storage.ExecutionRun) {
	t.Helper()
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		var snapshot *storage.ReviewSnapshot
		var prompt string
		switch event.Method {
		case "review/start":
			var params struct{ Target struct{ Instructions string } }
			if err := json.Unmarshal(event.Params, &params); err != nil {
				t.Fatal(err)
			}
			prompt = params.Target.Instructions
			_, encoded, ok := strings.Cut(prompt, "Checkpoint "+runReference(run)+":\n")
			if !ok || json.Unmarshal([]byte(encoded), &snapshot) != nil {
				t.Fatal("review omitted snapshot")
			}
		case "turn/start":
			var params struct {
				ThreadID string
				Input    []struct{ Text string }
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(params.ThreadID, "triage-thread-") {
				continue
			}
			prompt = params.Input[0].Text
			_, encoded, ok := strings.Cut(prompt, "Immutable input and current queue:\n")
			var input struct{ Snapshot *storage.ReviewSnapshot }
			if !ok || json.Unmarshal([]byte(encoded), &input) != nil {
				t.Fatal("triage omitted snapshot")
			}
			snapshot = input.Snapshot
		default:
			continue
		}
		assertReviewContexts(t, snapshot, runs)
		if !reflect.DeepEqual(snapshot, run.ReviewSnapshot) || strings.Contains(prompt, "LIVE CONTEXT MUST NOT APPEAR") || strings.Contains(prompt, "CHECKPOINT CONTEXT MUST NOT APPEAR") {
			t.Fatal("prompt replaced historical context")
		}
		for _, index := range []int{0, 2} {
			encoded, _ := json.Marshal(runs[index].GroupContext.Group.Context)
			if strings.Count(prompt, string(encoded)) != 1 {
				t.Fatal("captured document duplicated or lost")
			}
		}
		if !strings.Contains(prompt, "separately observed current code") || !strings.Contains(prompt, "never merge documents by name") {
			t.Fatal("context boundary missing")
		}
	}
}

func TestCheckpointReviewerPairsHistoricalContextsAndBindsCleanDigest(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, sameGroup := range []bool{true, false} {
		mode := "review_clean"
		if !sameGroup {
			mode = "review_findings"
		}
		t.Run(mode, func(t *testing.T) {
			s, request, _, runs, _, _ := preparedContextReviewCheckpoint(t, executable, mode, sameGroup)
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "completed" {
				t.Fatalf("review: %+v; %v", status, s.options.Supervisor.err)
			}
			run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
			if err != nil {
				t.Fatal(err)
			}
			assertReviewContexts(t, run.ReviewSnapshot, runs)
			assertCheckpointContextPrompts(t, s, run, runs)
			before, _ := expectedReviewCleanMarker(run.ReviewSnapshot)
			encoded, _ := json.Marshal(run.ReviewSnapshot)
			var changed storage.ReviewSnapshot
			if err := json.Unmarshal(encoded, &changed); err != nil {
				t.Fatal(err)
			}
			changed.GroupContexts[0].Snapshot.Group.Context += "changed"
			after, _ := expectedReviewCleanMarker(&changed)
			if before == after {
				t.Fatal("clean digest ignores captured requirements")
			}
		})
	}
}
