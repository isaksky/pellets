package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func checkpointTargets(pellets ...storage.Pellet) []storage.ReviewTargetVersion {
	var targets []storage.ReviewTargetVersion
	for _, p := range pellets {
		targets = append(targets, storage.ReviewTargetVersion{Reference: p.Reference, Version: storage.PelletVersion(p)})
	}
	return targets
}

func TestCheckpointExplicitInsertionChecksAnchorAndRebalancesAtomically(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	repo := f.open(t)
	defer repo.Close()
	a := addOrdinary(t, repo, f.main, "a")
	b := addOrdinary(t, repo, f.main, "b")
	c := addOrdinary(t, repo, f.main, "c")
	if _, err := repo.db.Exec(`UPDATE pellets SET priority=number WHERE project_id=?`, f.main.Project.ID); err != nil {
		t.Fatal(err)
	}
	b, _ = repo.ReadPellet(ctx, f.main, b.Reference)
	input := storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "explicit before", ReviewTargets: []domain.PelletReference{c.Reference}, Placement: &storage.PelletPlacement{Target: b.Reference, Before: true}, PlacementTargetVersion: storage.PelletVersion(b)}
	cp, err := repo.CreateWebPellet(ctx, f.main, input)
	if err != nil {
		t.Fatal(err)
	}
	assertActiveOrder(t, repo, f.main, a.Reference, cp.Reference, b.Reference, c.Reference)
	assertActivePriorityInvariants(t, repo.db, f.main.Project.ID, 4)
	if cp.Checkpoint.Targets[0].Number != c.Reference.Number {
		t.Fatal("explicit placement inferred scope from adjacency")
	}
	// Gap-exhaustion rebalancing changed the anchor version. Reusing the
	// pre-insertion anchor must not allocate another checkpoint or number.
	_, err = repo.CreateWebPellet(ctx, f.main, input)
	var conflict *storage.OptimisticConflict
	if !errors.As(err, &conflict) || conflict.Pellet.Reference != b.Reference {
		t.Fatalf("stale insertion anchor did not conflict: %v", err)
	}
	assertQueryInt(t, repo.db, `SELECT next_pellet_number FROM projects WHERE project_id=1`, 5)
	b, _ = repo.ReadPellet(ctx, f.main, b.Reference)
	input.Placement.Before = false
	input.PlacementTargetVersion = storage.PelletVersion(b)
	after, err := repo.CreateWebPellet(ctx, f.main, input)
	if err != nil {
		t.Fatal(err)
	}
	assertActiveOrder(t, repo, f.main, a.Reference, cp.Reference, b.Reference, after.Reference, c.Reference)
}

func TestCheckpointScopeEditsCheckEveryVersionAndPreserveOverlappingScope(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	repo := f.open(t)
	defer repo.Close()
	writer := &WebWriter{db: repo.db}
	a := addOrdinary(t, repo, f.main, "a")
	b := addOrdinary(t, repo, f.main, "b")
	c := addOrdinary(t, repo, f.main, "c")
	cp := addReview(t, repo, f.main, a, c)
	overlap := addReview(t, repo, f.main, a, b)
	staleB := b
	title := "new b scope"
	b, _ = repo.UpdatePellet(ctx, f.main, b.Reference, storage.PelletChanges{Title: &title})
	_, err := writer.UpdateWebCheckpointScope(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp), checkpointTargets(a, staleB))
	var conflict *storage.OptimisticConflict
	if !errors.As(err, &conflict) || conflict.Pellet.Reference != b.Reference {
		t.Fatalf("stale target accepted: %v", err)
	}
	assertQueryInt(t, repo.db, `SELECT count(*) FROM review_checkpoint_scope_history`, 0)
	changed, err := writer.UpdateWebCheckpointScope(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp), checkpointTargets(a, b))
	if err != nil {
		t.Fatal(err)
	}
	if changed.ImplementationRevision != cp.ImplementationRevision+1 || *changed.Priority != *cp.Priority || changed.Checkpoint.Targets[1].Number != b.Reference.Number || changed.Checkpoint.Targets[1].Title != title {
		t.Fatalf("scope generation or placement wrong: %+v", changed)
	}
	_, err = writer.UpdateWebCheckpointScope(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp), checkpointTargets(c))
	if !errors.As(err, &conflict) || conflict.Pellet.Reference != cp.Reference {
		t.Fatalf("stale checkpoint accepted: %v", err)
	}
	other, _ := repo.ReadPellet(ctx, f.main, overlap.Reference)
	if other.Checkpoint.Targets[0].Number != a.Reference.Number || other.Checkpoint.Targets[1].Number != b.Reference.Number || other.Checkpoint.Targets[1].Title != "b" {
		t.Fatal("editing one checkpoint changed an overlapping scope")
	}
	reader, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	history, err := reader.ReadWebCheckpointHistory(ctx, f.main.Project, cp.Reference)
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0].Scope, cp.Checkpoint) {
		t.Fatalf("old scope was not preserved exactly: %+v %v", history, err)
	}
	if _, err := repo.db.Exec(`UPDATE review_checkpoint_scope_history SET scope_json='null'`); err == nil {
		t.Fatal("historical scope is mutable")
	}
	_, err = writer.UpdateWebCheckpointScope(ctx, f.other.Project, cp.Reference, storage.PelletVersion(changed), checkpointTargets(a))
	assertPelletErrorCode(t, err, "reference_project_mismatch")
}

func TestCheckpointScopeEditRetainsCapturedExecutionAndRejectsRetargetedResume(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	repo := f.open(t)
	defer repo.Close()
	writer := &WebWriter{db: repo.db}
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := addOrdinary(t, repo, f.main, "implemented a")
	completeReviewTarget(t, repo, db, f.main, a)
	b := addOrdinary(t, repo, f.main, "implemented b")
	completeReviewTarget(t, repo, db, f.main, b)
	a, _ = repo.ReadPellet(ctx, f.main, a.Reference)
	b, _ = repo.ReadPellet(ctx, f.main, b.Reference)
	cp := addReview(t, repo, f.main, a)
	cp = transitionReviewTest(t, repo, f.main, cp, storage.PelletStart)
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, cp.Reference.Number)
	capture.Mode = "review_checkpoint"
	run, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	_, err = writer.UpdateWebCheckpointScope(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp), checkpointTargets(b))
	assertPelletErrorCode(t, err, "checkpoint_not_editable")
	_, err = writer.RemoveWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp))
	assertPelletErrorCode(t, err, "checkpoint_not_editable")
	run, err = db.InterruptExecutionRun(ctx, run.ID, run.Revision, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	encodedBefore, _ := json.Marshal(run)
	cp = transitionReviewTest(t, repo, f.main, cp, storage.PelletRelease)
	cp, err = writer.UpdateWebCheckpointScope(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp), checkpointTargets(b))
	if err != nil {
		t.Fatal(err)
	}
	if !cp.Checkpoint.Ready || cp.Checkpoint.Targets[0].Number != b.Reference.Number {
		t.Fatalf("new generation did not capture current evidence: %+v", cp.Checkpoint)
	}
	captured, err := db.ReadExecutionRun(ctx, run.ID)
	encodedAfter, _ := json.Marshal(captured)
	if err != nil || string(encodedBefore) != string(encodedAfter) {
		t.Fatal("scope edit mutated a captured execution attempt")
	}
	transitionReviewTest(t, repo, f.main, cp, storage.PelletStart)
	capture.ResumeFrom = &run.ID
	_, err = db.CreateExecutionRun(ctx, capture)
	assertPelletErrorCode(t, err, "execution_run_conflict")
}

func TestCheckpointRemoveRestorePreservesPositionWithoutReviewCompletion(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	repo := f.open(t)
	defer repo.Close()
	writer := &WebWriter{db: repo.db}
	a := addOrdinary(t, repo, f.main, "a")
	b := addOrdinary(t, repo, f.main, "b")
	cp := addReview(t, repo, f.main, a)
	removed, err := writer.RemoveWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp))
	if err != nil {
		t.Fatal(err)
	}
	if removed.Status != domain.PelletMaybeLater || removed.Priority != nil || removed.CompletedAt != nil || !removed.Checkpoint.Removed || !reflect.DeepEqual(removed.Checkpoint.Targets, cp.Checkpoint.Targets) {
		t.Fatalf("removal changed evidence or implied completion: %+v", removed)
	}
	assertActiveOrder(t, repo, f.main, a.Reference, b.Reference)
	if _, err := repo.db.Exec(`UPDATE pellets SET priority=number*100 WHERE project_id=? AND status IN ('open','in_progress')`, f.main.Project.ID); err != nil {
		t.Fatal(err)
	}
	restored, err := writer.RestoreWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(removed))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Checkpoint.Removed || restored.Status != domain.PelletOpen || restored.CompletedAt != nil {
		t.Fatalf("incorrect restored lifecycle: %+v", restored)
	}
	assertActiveOrder(t, repo, f.main, a.Reference, cp.Reference, b.Reference)
	// Removed next anchor falls back to the surviving previous anchor.
	removed, err = writer.RemoveWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(restored))
	if err != nil {
		t.Fatal(err)
	}
	transitionReviewTest(t, repo, f.main, b, storage.PelletDefer)
	_, err = writer.RestoreWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(removed))
	if err != nil {
		t.Fatal(err)
	}
	assertActiveOrder(t, repo, f.main, a.Reference, cp.Reference)
	current, _ := repo.ReadPellet(ctx, f.main, cp.Reference)
	removed, err = writer.RemoveWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(current))
	if err != nil {
		t.Fatal(err)
	}
	// CLI reopen intentionally retains CLI tail semantics and clears removal.
	current = transitionReviewTest(t, repo, f.main, removed, storage.PelletReopen)
	if current.Checkpoint.Removed {
		t.Fatal("CLI reopen left the checkpoint hidden")
	}
	assertQueryInt(t, repo.db, `SELECT count(*) FROM execution_runs`, 0)
}

func TestCheckpointConcurrentRemovalHasOneWinnerAndConflictingUndo(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	repo := f.open(t)
	defer repo.Close()
	a := addOrdinary(t, repo, f.main, "a")
	cp := addReview(t, repo, f.main, a)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			writer, err := OpenWebWriter(ctx, f.path)
			if err == nil {
				defer writer.Close()
				_, err = writer.RemoveWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(cp))
			}
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		var conflict *storage.OptimisticConflict
		if err == nil {
			successes++
		} else if errors.As(err, &conflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("remove winners=%d conflicts=%d", successes, conflicts)
	}
	removed, _ := repo.ReadPellet(ctx, f.main, cp.Reference)
	title := "edited while removed"
	_, err := repo.UpdatePellet(ctx, f.main, cp.Reference, storage.PelletChanges{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&WebWriter{db: repo.db}).RestoreWebCheckpoint(ctx, f.main.Project, cp.Reference, storage.PelletVersion(removed))
	var conflict *storage.OptimisticConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale undo accepted: %v", err)
	}
}

func TestCheckpointCompletedReviewHistorySurvivesReopenAndScopeEdit(t *testing.T) {
	ctx := context.Background()
	f, repo, db, run := triageFixture(t, nil)
	if _, err := db.CompleteReviewCheckpoint(ctx, run.ID, run.Revision); err != nil {
		t.Fatal(err)
	}
	ref := domain.PelletReference{ProjectCode: f.main.Project.Code, Number: run.PelletNumber}
	cp, err := repo.ReadPellet(ctx, f.main, ref)
	if err != nil {
		t.Fatal(err)
	}
	cp = transitionReviewTest(t, repo, f.main, cp, storage.PelletReopen)
	b := addOrdinary(t, repo, f.main, "new target")
	if _, err := (&WebWriter{db: repo.db}).UpdateWebCheckpointScope(ctx, f.main.Project, ref, storage.PelletVersion(cp), checkpointTargets(b)); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	history, err := reader.ReadWebCheckpointHistory(ctx, f.main.Project, ref)
	if err != nil || len(history) != 2 {
		t.Fatalf("reopened completed review missing from history: %+v %v", history, err)
	}
	outcome, err := reader.ReadCheckpointOutcome(ctx, run.ProjectID, run.PelletNumber, run.ImplementationRevision)
	if err != nil || !outcome.ReviewCompleted || outcome.Status != "clean" {
		t.Fatalf("completed review history changed: %+v %v", outcome, err)
	}
	if history[1].ImplementationRevision != run.ImplementationRevision || !storage.SameReviewScope(history[1].Scope, run.CheckpointScope) {
		t.Fatal("completed review scope receipt changed")
	}
}
