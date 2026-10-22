package sqlite

import (
	"context"
	"reflect"
	"testing"

	"pellets/internal/storage"
)

func TestLatestWebProjectRunsIsQueryOnlyCurrentAndProjectScoped(t *testing.T) {
	ctx := context.Background()
	db, first, fixture := createTestRun(t)
	first = updateRun(t, db, first, storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "unknown", Summary: "Previous attempt"}, "")
	latest, err := db.CreateExecutionRun(ctx, first.RunCapture)
	if err != nil {
		t.Fatal(err)
	}
	otherRuns := make(map[int64]storage.ExecutionRun)
	repo := fixture.open(t)
	for _, selected := range []storage.ResolvedProject{fixture.linked, fixture.other} {
		pellet, err := repo.CreatePellet(ctx, selected, storage.NewPellet{Title: "Another workspace"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.TransitionPellet(ctx, selected, pellet.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
			t.Fatal(err)
		}
		run, err := db.CreateExecutionRun(ctx, runCapture(selected.Project.ID, selected.Workspace.ID, pellet.Reference.Number))
		if err != nil {
			t.Fatal(err)
		}
		otherRuns[selected.Workspace.ID] = run
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenWebReader(ctx, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	// A single connection also catches nested queries with unclosed rows.
	reader.db.SetMaxOpenConns(1)
	assertPragmaInt(t, reader.db, "query_only", 1)
	before := queryDataVersion(t, db.db)
	check := func(want storage.ExecutionRun) {
		t.Helper()
		runs, err := reader.LatestWebProjectRuns(ctx, fixture.main.Project.ID)
		if err != nil || len(runs) != 2 || !reflect.DeepEqual(runs[want.WorkspaceID], want) {
			t.Fatalf("latest runs = %#v, %v; want latest run %d with its full evidence", runs, err, want.ID)
		}
		if !reflect.DeepEqual(runs[fixture.linked.Workspace.ID], otherRuns[fixture.linked.Workspace.ID]) {
			t.Fatal("another workspace's latest run was lost or mixed with this workspace")
		}
	}
	check(latest)
	for _, projectID := range []int64{fixture.other.Project.ID, 999999} {
		runs, err := reader.LatestWebProjectRuns(ctx, projectID)
		want := map[int64]storage.ExecutionRun{}
		if projectID == fixture.other.Project.ID {
			want[fixture.other.Workspace.ID] = otherRuns[fixture.other.Workspace.ID]
		}
		if err != nil || !reflect.DeepEqual(runs, want) {
			t.Fatalf("project %d leaked runs: %#v, %v", projectID, runs, err)
		}
	}
	if after := queryDataVersion(t, db.db); after != before {
		t.Fatalf("web run reads wrote to the database: data_version %d -> %d", before, after)
	}
	// The reader must not cache execution state across a later committed update.
	latest = updateRun(t, db, latest, storage.RunProgress{Phase: "implementation", State: "awaiting_input", Summary: "Waiting for input"}, "")
	check(latest)
	if _, err := reader.LatestWebProjectRuns(ctx, 0); err == nil {
		t.Fatal("accepted invalid project ID")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reader.LatestWebProjectRuns(canceled, fixture.main.Project.ID); err == nil {
		t.Fatal("ignored request cancellation")
	}
}
