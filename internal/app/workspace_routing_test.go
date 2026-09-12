package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func schedulerRouting(t *testing.T, s *Scheduler, r ScheduleRequest) (*sqlite.WebReader, *sqlite.WebWriter, storage.ProjectRouting) {
	t.Helper()
	ctx := context.Background()
	reader, err := sqlite.OpenWebReader(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	writer, err := sqlite.OpenWebWriter(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	routing, err := reader.ReadProjectRouting(ctx, r.Selected.Project)
	if err != nil {
		t.Fatal(err)
	}
	return reader, writer, routing
}
func TestWorkspaceAssignmentsWakeWaitingScheduleUsingNewClaimPolicy(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, q := schedulerFixture(t, executable, "schedule_success")
	_, writer, routing := schedulerRouting(t, s, r)
	ctx := context.Background()
	r.Mode, r.Limit, r.UseWorkspaceAssignments = "watch", 1, true
	changes := make(chan struct{}, 1)
	s.options.Subscribe = func() (<-chan struct{}, func()) { return changes, func() {} }
	routing, err := writer.SaveWorkspaceAssignment(ctx, r.Selected.Project, r.Selected.Workspace.ID, routing.Version, storage.WorkspaceAssignment{Mode: "explicit", Groups: []string{"web-ui"}})
	if err != nil {
		t.Fatal(err)
	}
	h := startSchedule(t, s, r)
	awaitScheduleState(t, h, "waiting")
	// Existing ungrouped work becomes eligible on the next pickup after opt-out.
	if _, err = writer.SetGroupAssignments(ctx, r.Selected.Project, routing.Version, false); err != nil {
		t.Fatal(err)
	}
	changes <- struct{}{}
	result := awaitSchedule(t, h)
	if result.Completed != 1 || result.Reason != "limit_reached" {
		t.Fatalf("watch did not reroute next claim: %+v", result)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, result.RunID)
	if err != nil || run.WorkspaceSelection == nil || run.WorkspaceSelection.Enabled {
		t.Fatalf("atomic policy capture: %+v %v", run.WorkspaceSelection, err)
	}
	p, err := q.ReadPellet(ctx, r.Selected, domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1})
	if err != nil || p.Status != domain.PelletClosed {
		t.Fatalf("work incomplete: %+v %v", p, err)
	}
}
func TestWorkspaceAssignmentChangesPreserveExactResumeAndCapturedPolicy(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, _ := schedulerFixture(t, executable, "schedule_failed")
	_, writer, routing := schedulerRouting(t, s, r)
	ctx := context.Background()
	r.UseWorkspaceAssignments = true
	first := awaitSchedule(t, startSchedule(t, s, r))
	if first.RunID == 0 || first.State != "needs_attention" {
		t.Fatalf("failed attempt: %+v", first)
	}
	previous, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, first.RunID)
	if err != nil || previous.WorkspaceSelection == nil {
		t.Fatalf("missing routing capture: %v", err)
	}
	_, err = writer.SaveWorkspaceAssignment(ctx, r.Selected.Project, r.Selected.Workspace.ID, routing.Version, storage.WorkspaceAssignment{Mode: "explicit", Groups: []string{"unrelated"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	r.ResumePellet, r.ResumeFrom = &previous.PelletNumber, &previous.ID
	// Even forged continuation intent is replaced from the exact durable attempt.
	r.UseWorkspaceAssignments = false
	r.SavedWorkspaceSelection = &storage.WorkspaceSelection{Mode: "explicit", Groups: []string{"forged"}}
	second := awaitSchedule(t, startSchedule(t, s, r))
	if second.State != "completed" {
		t.Fatalf("resume failed after reassignment: %+v", second)
	}
	resumed, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, second.RunID)
	if err != nil || resumed.PelletNumber != previous.PelletNumber || !reflect.DeepEqual(resumed.WorkspaceSelection, previous.WorkspaceSelection) {
		t.Fatalf("resume selector changed: %+v %v", resumed, err)
	}
}
