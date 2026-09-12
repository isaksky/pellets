package sqlite

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func routingFixture(t *testing.T, f pelletRepositoryFixture) (*WebReader, *WebWriter, storage.ProjectRouting) {
	t.Helper()
	ctx := context.Background()
	reader, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	writer, err := OpenWebWriter(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	r, err := reader.ReadProjectRouting(ctx, f.main.Project)
	if err != nil {
		t.Fatal(err)
	}
	return reader, writer, r
}
func saveAssignment(t *testing.T, w *WebWriter, p storage.Project, r storage.ProjectRouting, a storage.WorkspaceAssignment) storage.ProjectRouting {
	t.Helper()
	updated, err := w.SaveWorkspaceAssignment(context.Background(), p, a.WorkspaceID, r.Version, a)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}
func TestWorkspaceRoutingPersistenceExactGroupsOverlapAndProjectIsolation(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	reader, writer, r := routingFixture(t, f)
	ctx := context.Background()
	if !r.Enabled || len(r.Assignments) != 2 || !r.Selection(f.main.Workspace.ID).AcceptsGroup(nil) {
		t.Fatalf("defaults: %+v", r)
	}
	web, space, other := "web-ui", " web-ui ", "other"
	r = saveAssignment(t, writer, f.main.Project, r, storage.WorkspaceAssignment{WorkspaceID: f.linked.Workspace.ID, Mode: "explicit", Groups: []string{web, space, web}})
	if r.Selection(f.main.Workspace.ID).AcceptsGroup(&web) || !r.Selection(f.main.Workspace.ID).AcceptsGroup(&other) || r.Selection(f.linked.Workspace.ID).AcceptsGroup(nil) {
		t.Fatalf("remaining/ungrouped: %+v", r)
	}
	r = saveAssignment(t, writer, f.main.Project, r, storage.WorkspaceAssignment{WorkspaceID: f.main.Workspace.ID, Mode: "explicit", Groups: []string{web}, IncludeUngrouped: true})
	if !r.Selection(f.main.Workspace.ID).AcceptsGroup(&web) || !r.Selection(f.linked.Workspace.ID).AcceptsGroup(&web) || r.Selection(f.main.Workspace.ID).AcceptsGroup(&space) {
		t.Fatal("overlap or exact bytes lost")
	}
	saved := r.Assignments
	r, err := writer.SetGroupAssignments(ctx, f.main.Project, r.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Selection(f.linked.Workspace.ID).AcceptsGroup(nil) || !r.Selection(f.main.Workspace.ID).AcceptsGroup(&other) || !reflect.DeepEqual(saved, r.Assignments) {
		t.Fatal("opt out did not preserve assignment")
	}
	reread, err := reader.ReadProjectRouting(ctx, f.main.Project)
	if err != nil || !reflect.DeepEqual(r, reread) {
		t.Fatalf("roundtrip: %+v %v", reread, err)
	}
	foreign, err := reader.ReadProjectRouting(ctx, f.other.Project)
	if err != nil || !foreign.Enabled {
		t.Fatal("cross-project opt out")
	}
	if _, err := writer.SaveWorkspaceAssignment(ctx, f.main.Project, f.other.Workspace.ID, r.Version, storage.DefaultWorkspaceAssignment(f.other.Workspace.ID)); domain.PublicError(err).Code != "workspace_not_registered" {
		t.Fatalf("foreign assignment: %v", err)
	}
}
func TestWorkspaceRoutingConcurrentEditorsConflict(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	reader, writer, r := routingFixture(t, f)
	other, err := OpenWebWriter(context.Background(), f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, w := range []*WebWriter{writer, other} {
		wg.Add(1)
		go func(w *WebWriter) {
			defer wg.Done()
			<-start
			_, err := w.SetGroupAssignments(context.Background(), f.main.Project, r.Version, false)
			results <- err
		}(w)
	}
	close(start)
	wg.Wait()
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if domain.PublicError(err).Code == "workspace_assignment_conflict" {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	current, err := reader.ReadProjectRouting(context.Background(), f.main.Project)
	if err != nil || current.Enabled {
		t.Fatal("lost update")
	}
}
func TestWorkspaceRoutingAtomicSelectionOwnedResumeAndCLICompatibility(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	_, writer, r := routingFixture(t, f)
	q := f.open(t)
	defer q.Close()
	ctx := context.Background()
	web, other := "web-ui", "other"
	a := storage.WorkspaceAssignment{WorkspaceID: f.linked.Workspace.ID, Mode: "explicit", Groups: []string{web}}
	r = saveAssignment(t, writer, f.main.Project, r, a)
	first, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "web", Group: &web})
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "other", Group: &other})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{UseWorkspaceAssignments: true})
	if err != nil || selected.Pellet == nil || selected.Pellet.Reference != second.Reference {
		t.Fatalf("remaining claim: %+v %v", selected, err)
	}
	captured := selected.WorkspaceSelection
	r = saveAssignment(t, writer, f.main.Project, r, storage.WorkspaceAssignment{WorkspaceID: f.main.Workspace.ID, Mode: "explicit", Groups: []string{web}})
	resumed, err := q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{UseWorkspaceAssignments: true, SavedWorkspaceSelection: captured, ResumePellet: &second.Reference.Number})
	if err != nil || resumed.Pellet.Reference != second.Reference || !reflect.DeepEqual(resumed.WorkspaceSelection, captured) {
		t.Fatalf("owned selection changed: %+v %v", resumed, err)
	}
	if !r.Accepts(f.main.Workspace.ID, *resumed.Pellet) {
		t.Fatal("routing hid owned work")
	}
	_, err = q.TransitionPellet(ctx, f.main, second.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletRelease})
	if err != nil {
		t.Fatal(err)
	}
	// CLI exact filters still select other, despite the explicit web-only assignment.
	cli, err := q.StartNextPellet(ctx, f.main, nil, &other)
	if err != nil || cli.Pellet.Reference != second.Reference {
		t.Fatalf("CLI narrowed by routing: %+v %v", cli, err)
	}
	next, err := q.SelectScheduledPellet(ctx, f.linked, storage.ScheduleSelection{UseWorkspaceAssignments: true})
	if err != nil || next.Pellet.Reference != first.Reference {
		t.Fatalf("explicit claim: %+v %v", next, err)
	}
}
func TestWorkspaceRoutingCapturesSelectionInDurableResume(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	_, writer, r := routingFixture(t, f)
	q := f.open(t)
	defer q.Close()
	ctx := context.Background()
	p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := q.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{UseWorkspaceAssignments: true})
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenExecutionRunDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, p.Reference.Number)
	capture.WorkspaceSelection = selected.WorkspaceSelection
	first, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	first = updateRun(t, db, first, storage.RunProgress{Phase: "preflight", State: "interrupted", Outcome: "unknown"}, "")
	saveAssignment(t, writer, f.main.Project, r, storage.WorkspaceAssignment{WorkspaceID: f.main.Workspace.ID, Mode: "explicit", Groups: []string{"different"}})
	capture = first.RunCapture
	capture.ResumeFrom = &first.ID
	capture.WorkspaceSelection = &storage.WorkspaceSelection{Mode: "explicit", Groups: []string{"injected"}}
	resumed, err := db.CreateExecutionRun(ctx, capture)
	if err != nil || !reflect.DeepEqual(resumed.WorkspaceSelection, first.WorkspaceSelection) {
		t.Fatalf("resume retargeted: %+v %v", resumed.WorkspaceSelection, err)
	}
}
