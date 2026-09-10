package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func addReview(t *testing.T, repo *PelletRepository, project storage.ResolvedProject, targets ...storage.Pellet) storage.Pellet {
	t.Helper()
	refs := make([]domain.PelletReference, len(targets))
	for i, p := range targets {
		refs[i] = p.Reference
	}
	p, err := repo.CreatePellet(context.Background(), project, storage.NewPellet{Title: "Review selected implementation", Kind: domain.PelletReviewCheckpoint, ReviewTargets: refs})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func addOrdinary(t *testing.T, repo *PelletRepository, project storage.ResolvedProject, title string) storage.Pellet {
	t.Helper()
	p, err := repo.CreatePellet(context.Background(), project, storage.NewPellet{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func transitionReviewTest(t *testing.T, repo *PelletRepository, project storage.ResolvedProject, p storage.Pellet, op storage.PelletLifecycleOperation) storage.Pellet {
	t.Helper()
	result, err := repo.TransitionPellet(context.Background(), project, p.Reference, storage.PelletLifecycleRequest{Operation: op})
	if err != nil {
		t.Fatal(err)
	}
	return result.Pellet
}

func completeReviewTarget(t *testing.T, repo *PelletRepository, db *ProjectDatabase, project storage.ResolvedProject, p storage.Pellet) storage.ExecutionRun {
	t.Helper()
	p = transitionReviewTest(t, repo, project, p, storage.PelletStart)
	run, err := db.CreateExecutionRun(context.Background(), runCapture(project.Project.ID, project.Workspace.ID, p.Reference.Number))
	if err != nil {
		t.Fatal(err)
	}
	progress := storage.RunProgress{Phase: "close", State: "running", ThreadID: "implementation-thread", TurnID: "implementation-turn", Finalization: &storage.FinalizationEvidence{Files: []string{"source.go"}, Tree: strings.Repeat("c", 40), Subject: p.Reference.String() + ": implement"}}
	run = updateRun(t, db, run, progress, strings.Repeat("b", 40))
	transitionReviewTest(t, repo, project, p, storage.PelletClose)
	progress.Phase, progress.State, progress.Outcome = "finalization", "completed", "succeeded"
	return updateRun(t, db, run, progress, "")
}

func TestReviewCheckpointInsertionRebalanceAndExactScope(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	a := addOrdinary(t, r, f.main, "a")
	b := addOrdinary(t, r, f.main, "b")
	c := addOrdinary(t, r, f.main, "c")
	d := addOrdinary(t, r, f.main, "d")
	// Force exhausted adjacency, with a selected target owned by another worktree.
	transitionReviewTest(t, r, f.linked, c, storage.PelletStart)
	if _, err := r.db.Exec(`UPDATE pellets SET priority=number WHERE project_id=?`, f.main.Project.ID); err != nil {
		t.Fatal(err)
	}
	cp := addReview(t, r, f.main, c, a)
	assertActiveOrder(t, r, f.main, a.Reference, b.Reference, c.Reference, cp.Reference, d.Reference)
	assertActivePriorityInvariants(t, r.db, f.main.Project.ID, 5)
	if cp.Checkpoint.Ready || len(cp.Checkpoint.Targets) != 2 || cp.Checkpoint.Targets[0].Number != a.Reference.Number || cp.Checkpoint.Targets[1].Number != c.Reference.Number {
		t.Fatalf("wrong explicit scope: %+v", cp.Checkpoint)
	}
	if _, err := r.MovePellet(context.Background(), f.main, cp.Reference, storage.PelletPlacement{Target: b.Reference, Before: true}); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadPellet(context.Background(), f.main, cp.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Checkpoint, cp.Checkpoint) {
		t.Fatal("move changed selected scope")
	}
	closed := transitionReviewTest(t, r, f.main, a, storage.PelletClose)
	appendCP := addReview(t, r, f.main, closed)
	assertActiveOrder(t, r, f.main, cp.Reference, b.Reference, c.Reference, d.Reference, appendCP.Reference)
}

func TestReviewCheckpointRejectsInvalidSelectionsAtomically(t *testing.T) {
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	a := addOrdinary(t, r, f.main, "a")
	foreign := addOrdinary(t, r, f.other, "foreign")
	cp := addReview(t, r, f.main, a)
	for _, input := range []storage.NewPellet{
		{Kind: domain.PelletReviewCheckpoint},
		{Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{a.Reference, a.Reference}},
		{Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{foreign.Reference}},
		{Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{cp.Reference}},
		{Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{{ProjectCode: "code", Number: 999}}},
		{Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{a.Reference}, Status: domain.PelletMaybeLater},
		{Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{a.Reference}, Placement: &storage.PelletPlacement{Target: a.Reference}},
		{ReviewTargets: []domain.PelletReference{a.Reference}},
		{Kind: "dependency"},
	} {
		input.Title = "invalid"
		if _, err := r.CreatePellet(context.Background(), f.main, input); err == nil {
			t.Fatalf("accepted invalid checkpoint: %+v", input)
		}
	}
	assertQueryInt(t, r.db, `SELECT count(*) FROM pellets WHERE project_id=1`, 2)
	assertQueryInt(t, r.db, `SELECT next_pellet_number FROM projects WHERE project_id=1`, 3)
}

func TestReviewCheckpointReadinessAcrossWorktreesAndGenerations(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := addOrdinary(t, r, f.main, "a")
	b := addOrdinary(t, r, f.main, "b")
	cp := addReview(t, r, f.main, a, b)
	unrelated := addOrdinary(t, r, f.main, "unrelated")
	transitionReviewTest(t, r, f.main, a, storage.PelletClose)
	transitionReviewTest(t, r, f.main, b, storage.PelletDefer)
	for _, op := range []storage.PelletLifecycleOperation{storage.PelletStart, storage.PelletClose} {
		_, err := r.TransitionPellet(ctx, f.main, cp.Reference, storage.PelletLifecycleRequest{Operation: op})
		assertPelletErrorCode(t, err, "review_checkpoint_not_ready")
	}
	for _, atomic := range []bool{false, true} {
		var s storage.NextSelection
		if atomic {
			s, err = r.StartNextPellet(ctx, f.main, nil, nil)
		} else {
			s, err = r.NextPellet(ctx, f.main, nil, nil)
		}
		if err != nil || s.Pellet == nil || s.Pellet.Reference != unrelated.Reference {
			t.Fatalf("waiting checkpoint blocked unrelated work: %+v %v", s, err)
		}
	}
	transitionReviewTest(t, r, f.main, unrelated, storage.PelletClose)
	transitionReviewTest(t, r, f.main, a, storage.PelletReopen)
	transitionReviewTest(t, r, f.main, b, storage.PelletReopen)
	runA := completeReviewTarget(t, r, db, f.linked, a)
	completeReviewTarget(t, r, db, f.main, b)
	read := func() storage.Pellet {
		p, err := r.ReadPellet(ctx, f.main, cp.Reference)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ready := read()
	if !ready.Checkpoint.Ready || ready.Checkpoint.Targets[0].Evidence.RunID != runA.ID || ready.Checkpoint.Targets[0].Evidence.WorkspaceID != f.linked.Workspace.ID {
		t.Fatalf("missing cross-worktree evidence: %+v", ready.Checkpoint)
	}
	transitionReviewTest(t, r, f.main, a, storage.PelletReopen)
	transitionReviewTest(t, r, f.main, a, storage.PelletClose)
	if p := read(); p.Checkpoint.Ready || p.Checkpoint.Targets[0].Reason != "evidence_missing" {
		t.Fatalf("old generation reused: %+v", p.Checkpoint)
	}
	transitionReviewTest(t, r, f.main, a, storage.PelletReopen)
	completeReviewTarget(t, r, db, f.linked, a)
	if !read().Checkpoint.Ready {
		t.Fatal("fresh evidence did not restore readiness")
	}
	title := "changed scope"
	if _, err := r.UpdatePellet(ctx, f.main, b.Reference, storage.PelletChanges{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.Checkpoint.Ready || p.Checkpoint.Targets[1].Reason != "scope_changed" {
		t.Fatalf("scope edit accepted: %+v", p.Checkpoint)
	}
	if _, err := r.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	p := read()
	if p.Checkpoint.Ready || p.Checkpoint.Targets[0].Reason != "target_missing" || p.Checkpoint.Targets[0].Title != "a" {
		t.Fatalf("purge lost diagnostic scope: %+v", p.Checkpoint)
	}
}

func TestReviewCheckpointRunRejectsStaleCompletionAndResume(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := addOrdinary(t, r, f.main, "a")
	completeReviewTarget(t, r, db, f.linked, a)
	cp := addReview(t, r, f.main, a)
	transitionReviewTest(t, r, f.main, cp, storage.PelletStart)
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, cp.Reference.Number)
	if _, err := db.CreateExecutionRun(ctx, capture); err == nil {
		t.Fatal("checkpoint captured as implementation")
	}
	capture.Mode = "review_checkpoint"
	run, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if run.CheckpointScope == nil || !run.CheckpointScope.Ready {
		t.Fatal("review scope not captured")
	}
	transitionReviewTest(t, r, f.linked, a, storage.PelletReopen)
	completeReviewTarget(t, r, db, f.linked, a)
	_, err = r.TransitionPellet(ctx, f.main, cp.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletClose})
	assertPelletErrorCode(t, err, "execution_run_conflict")
	progress := storage.RunProgress{Phase: "review", State: "completed", Outcome: "succeeded", ThreadID: "review-thread", TurnID: "review-turn"}
	_, err = db.UpdateExecutionRun(ctx, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress, VerifiedCommit: strings.Repeat("d", 40)})
	assertPelletErrorCode(t, err, "execution_run_conflict")
	progress.State, progress.Outcome = "interrupted", "unknown"
	run = updateRun(t, db, run, progress, "")
	capture.ResumeFrom = &run.ID
	_, err = db.CreateExecutionRun(ctx, capture)
	assertPelletErrorCode(t, err, "execution_run_conflict")
}

func TestReviewCheckpointPurgeRetainsExactEvidenceWithoutRevivingStaleGeneration(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := addOrdinary(t, r, f.main, "current evidence")
	b := addOrdinary(t, r, f.main, "stale evidence")
	cp := addReview(t, r, f.main, a, b)
	completeReviewTarget(t, r, db, f.linked, a)
	transitionReviewTest(t, r, f.main, a, storage.PelletReopen)
	latest := completeReviewTarget(t, r, db, f.linked, a)
	completeReviewTarget(t, r, db, f.main, b)
	transitionReviewTest(t, r, f.main, b, storage.PelletReopen)
	transitionReviewTest(t, r, f.main, b, storage.PelletClose)
	before, err := r.ReadPellet(ctx, f.main, cp.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if before.Checkpoint.Targets[0].Evidence == nil || before.Checkpoint.Targets[0].Evidence.RunID != latest.ID || before.Checkpoint.Targets[1].Evidence != nil {
		t.Fatalf("invalid pre-purge evidence: %+v", before.Checkpoint)
	}
	if _, err := r.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	// A fresh connection verifies the receipt association survived the purge
	// transaction, not just a previously materialized read or creation result.
	reopened := f.open(t)
	defer reopened.Close()
	show, err := reopened.ReadPellet(ctx, f.main, cp.Reference)
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.ListPellets(ctx, f.main, storage.PelletListOptions{})
	if err != nil || len(list) != 1 {
		t.Fatalf("post-purge list: %+v %v", list, err)
	}
	web, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer web.Close()
	inspector, err := web.ReadWebPellet(ctx, f.main.Project, cp.Reference)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []storage.Pellet{show, list[0], inspector} {
		if p.Checkpoint.Ready {
			t.Fatal("purged target became ready")
		}
		for _, target := range p.Checkpoint.Targets {
			if target.Reason != "target_missing" || target.Status != nil {
				t.Fatalf("purged target lost missing state: %+v", target)
			}
		}
		if !reflect.DeepEqual(p.Checkpoint.Targets[0].Evidence, before.Checkpoint.Targets[0].Evidence) {
			t.Fatalf("purge replaced exact receipt: got %+v want %+v", p.Checkpoint.Targets[0].Evidence, before.Checkpoint.Targets[0].Evidence)
		}
		if p.Checkpoint.Targets[1].Evidence != nil {
			t.Fatalf("purge revived stale evidence: %+v", p.Checkpoint.Targets[1])
		}
	}
}

func TestReviewCheckpointMigrationRollbackAndReadSerialization(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	old, err := openWithMigrations(ctx, path, migrations[:11])
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	sequence := append([]migration(nil), migrations...)
	sequence[11].assert = func(context.Context, *sql.Conn) error { return errors.New("injected migration failure") }
	if db, err := openWithMigrations(ctx, path, sequence); err == nil || db != nil {
		t.Fatal("failed checkpoint migration committed")
	}
	raw := openRawDatabase(t, path)
	assertPragmaInt(t, raw, "user_version", 11)
	assertQueryInt(t, raw, `SELECT count(*) FROM pragma_table_info('pellets') WHERE name='kind'`, 0)
	raw.Close()
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	a := addOrdinary(t, r, f.main, "a")
	cp := addReview(t, r, f.main, a)
	search, err := r.SearchPellets(ctx, f.main, storage.PelletSearchOptions{Query: "Review"})
	if err != nil || len(search) != 1 || !reflect.DeepEqual(search[0].Checkpoint, cp.Checkpoint) {
		t.Fatalf("search lost checkpoint metadata: %+v %v", search, err)
	}
	encoded, err := json.Marshal(cp.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var decoded storage.ReviewCheckpoint
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(&decoded, cp.Checkpoint) {
		t.Fatalf("checkpoint serialization: %s %v", encoded, err)
	}
}

func TestReviewCheckpointConcurrentInsertionUsesAuthoritativeOrder(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	other := f.open(t)
	defer other.Close()
	a := addOrdinary(t, r, f.main, "a")
	b := addOrdinary(t, r, f.main, "b")
	c := addOrdinary(t, r, f.main, "c")
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, repo := range []*PelletRepository{r, other} {
		wg.Add(1)
		go func(repo *PelletRepository) {
			defer wg.Done()
			<-start
			_, err := repo.CreatePellet(ctx, f.main, storage.NewPellet{Title: "race review", Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{a.Reference, b.Reference}})
			errs <- err
		}(repo)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.ListPellets(ctx, f.main, storage.PelletListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0].Reference != a.Reference || rows[1].Reference != b.Reference || rows[4].Reference != c.Reference || rows[2].Kind != domain.PelletReviewCheckpoint || rows[3].Kind != domain.PelletReviewCheckpoint {
		t.Fatalf("raced insertion order: %+v", rows)
	}
	assertActivePriorityInvariants(t, r.db, f.main.Project.ID, 5)
}

func TestReviewCheckpointFiltersAndRenameDoNotChangeReviewScope(t *testing.T) {
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	r := f.open(t)
	defer r.Close()
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := addOrdinary(t, r, f.main, "a")
	completeReviewTarget(t, r, db, f.linked, a)
	cp := addReview(t, r, f.main, a)
	group, external := "Group/Exact", "Issue:Exact"
	cp, err = r.UpdatePellet(ctx, f.main, cp.Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &group}, ExternalID: storage.NullableTextChange{Set: true, Value: &external}})
	if err != nil {
		t.Fatal(err)
	}
	otherCase := "group/exact"
	for _, atomic := range []bool{false, true} {
		var next storage.NextSelection
		if atomic {
			next, err = r.StartNextPellet(ctx, f.main, &external, &otherCase)
		} else {
			next, err = r.NextPellet(ctx, f.main, &external, &otherCase)
		}
		if err != nil || next.Pellet != nil {
			t.Fatalf("inexact checkpoint filter: %+v %v", next, err)
		}
	}
	next, err := r.SelectScheduledPellet(ctx, f.main, storage.ScheduleSelection{ExternalID: &external, Group: &group})
	if err != nil || next.Pellet == nil || next.Pellet.Reference != cp.Reference {
		t.Fatalf("exact schedule did not select ready checkpoint: %+v %v", next, err)
	}
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, cp.Reference.Number)
	capture.Mode = "review_checkpoint"
	capture.ExternalID, capture.Group = &external, &group
	run, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := db.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: f.main.Project.ID, NewCode: "renamed"})
	if err != nil {
		t.Fatal(err)
	}
	selected := f.main
	selected.Project = renamed.Project
	current, err := r.ReadPellet(ctx, selected, cp.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !current.Checkpoint.Ready || current.Checkpoint.Targets[0].Reference != "renamed-1" || current.Checkpoint.Targets[0].SelectedReference != "code-1" || !storage.SameReviewScope(current.Checkpoint, run.CheckpointScope) {
		t.Fatalf("rename changed identity: %+v", current.Checkpoint)
	}
	transitionReviewTest(t, r, selected, current, storage.PelletClose)
	completed := updateRun(t, db, run, storage.RunProgress{Phase: "review", State: "completed", Outcome: "succeeded", ThreadID: "review-thread", TurnID: "review-turn"}, strings.Repeat("d", 40))
	if completed.State != "completed" {
		t.Fatal("rename prevented review completion")
	}
}
