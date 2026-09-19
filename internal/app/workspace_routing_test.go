package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/discovery"
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
func TestWorkspaceAssignmentsWakeWaitingScheduleWithMainFallback(t *testing.T) {
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
	linkedRoot := filepath.Join(t.TempDir(), "linked")
	gitForExecutionTest(t, s.options.Database.Root, "worktree", "add", "-b", "routing-linked", linkedRoot)
	identity, err := discovery.FindGitIdentity(ctx, linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	rootPath, err := discovery.NormalizeLocalPath(s.options.Database.Root, identity.WorkTreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := discovery.NormalizeLocalPath(s.options.Database.Root, identity.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := sqlite.OpenProjectDatabase(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := projects.RegisterProject(ctx, storage.ProjectRegistration{Code: r.Selected.Project.Code, GitCommonDir: r.Selected.Project.GitCommonDir, GitDir: gitDir, WorkspaceRoot: rootPath})
	projects.Close()
	if err != nil {
		t.Fatal(err)
	}
	var linkedID int64
	for _, w := range project.Workspaces {
		if w.GitDir == gitDir {
			linkedID = w.ID
		}
	}
	reader, err := sqlite.OpenWebReader(ctx, s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	routing, err = reader.ReadProjectRouting(ctx, project)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.SaveWorkspaceAssignment(ctx, project, linkedID, routing.Version, storage.WorkspaceAssignment{Mode: "explicit", IncludeUngrouped: true}); err != nil {
		t.Fatal(err)
	}
	h := startSchedule(t, s, r)
	awaitScheduleState(t, h, "waiting")
	// Git can remove the preferred recipient without editing any Pellets preferences.
	gitForExecutionTest(t, s.options.Database.Root, "worktree", "remove", linkedRoot)
	changes <- struct{}{}
	result := awaitSchedule(t, h)
	if result.Completed != 1 || result.Reason != "limit_reached" {
		t.Fatalf("watch did not reroute next claim: %+v", result)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, result.RunID)
	if err != nil || run.WorkspaceSelection == nil || !run.WorkspaceSelection.Enabled || !run.WorkspaceSelection.IncludeUngrouped {
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
