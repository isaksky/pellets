package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestLiveChangesRetainExactEditsAndAdoptInOrder(t *testing.T) {
	ctx := context.Background()
	db, run, f := createTestRun(t)
	q := f.open(t)
	defer q.Close()
	ref := domain.PelletReference{ProjectCode: f.main.Project.Code, Number: run.PelletNumber}
	for _, description := range []string{"Require keyboard support", "Require keyboard and touch support"} {
		if _, err := q.UpdatePellet(ctx, f.main, ref, storage.PelletChanges{Description: &description}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := db.PendingExecutionChange(ctx, run.ID)
	if err != nil || first == nil {
		t.Fatalf("pending: %+v %v", first, err)
	}
	var old struct{ Description string }
	json.Unmarshal(first.Old, &old)
	if old.Description != run.PelletDescription {
		t.Fatalf("lost original: %s", first.Old)
	}
	if _, err = db.AdoptExecutionChange(ctx, *first); err == nil {
		t.Fatal("adopted without assessment")
	}
	a := storage.ChangeAssessment{Significant: true, Reason: "New acceptance criteria", FollowUp: "Implement keyboard support", ThreadID: "assessor", TurnID: "assessment"}
	if err = db.AssessExecutionChange(ctx, *first, a); err != nil {
		t.Fatal(err)
	}
	updated, err := db.AdoptExecutionChange(ctx, *first)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ImplementationRevision != run.ImplementationRevision+1 || updated.PelletDescription != "Require keyboard support" || updated.ThreadID != run.ThreadID {
		t.Fatalf("incorrect adoption: %+v", updated)
	}
	if _, err = db.AdoptExecutionChange(ctx, *first); err == nil {
		t.Fatal("replayed adoption")
	}
	second, err := db.PendingExecutionChange(ctx, run.ID)
	if err != nil || second == nil || second.FromRevision != first.ToRevision {
		t.Fatalf("second edit: %+v %v", second, err)
	}
	if err = db.AssessExecutionChange(ctx, *second, a); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AdoptExecutionChange(ctx, *second); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingExecutionChange(ctx, run.ID)
	if err != nil || pending != nil {
		t.Fatalf("remaining: %+v %v", pending, err)
	}
	assertQueryInt(t, db.db, `SELECT count(*) FROM execution_changes WHERE delivered=1`, 2)
}

func TestLiveChangesCannotBridgeReleaseAndReclaim(t *testing.T) {
	ctx := context.Background()
	db, run, f := createTestRun(t)
	q := f.open(t)
	defer q.Close()
	ref := domain.PelletReference{ProjectCode: f.main.Project.Code, Number: run.PelletNumber}
	title := "Edited while running"
	if _, err := q.UpdatePellet(ctx, f.main, ref, storage.PelletChanges{Title: &title}); err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingExecutionChange(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AssessExecutionChange(ctx, *pending, storage.ChangeAssessment{Reason: "Cosmetic", ThreadID: "assessor", TurnID: "assessment"}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []storage.PelletLifecycleOperation{storage.PelletRelease, storage.PelletStart} {
		if _, err = q.TransitionPellet(ctx, f.main, ref, storage.PelletLifecycleRequest{Operation: op}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.PendingExecutionChange(ctx, run.ID); err == nil {
		t.Fatal("accepted a replaced generation")
	}
	if _, err = db.AdoptExecutionChange(ctx, *pending); err == nil {
		t.Fatal("late assessment adopted after release/reclaim")
	}
}
