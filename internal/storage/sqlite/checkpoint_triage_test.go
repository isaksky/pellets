package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func triageFixture(t *testing.T, findings []storage.ReviewFinding) (*pelletRepositoryFixture, *PelletRepository, *ProjectDatabase, storage.ExecutionRun) {
	t.Helper()
	ctx := context.Background()
	f := newPelletRepositoryFixture(t)
	repo := f.open(t)
	t.Cleanup(func() { repo.Close() })
	db, err := OpenProjectDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target := addOrdinary(t, repo, f.main, "target")
	completeReviewTarget(t, repo, db, f.main, target)
	cp := addReview(t, repo, f.main, target)
	group, external := "Group/Exact", "Issue:Exact"
	cp, err = repo.UpdatePellet(ctx, f.main, cp.Reference, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &group}, ExternalID: storage.NullableTextChange{Set: true, Value: &external}})
	if err != nil {
		t.Fatal(err)
	}
	cp = transitionReviewTest(t, repo, f.main, cp, storage.PelletStart)
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, cp.Reference.Number)
	capture.Mode = "review_checkpoint"
	run, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	e := cp.Checkpoint.Targets[0].Evidence
	status := "findings"
	if len(findings) == 0 {
		status = "clean"
	}
	run = updateRun(t, db, run, storage.RunProgress{Phase: "review", State: "running", ThreadID: "review-thread", TurnID: "review-turn", ReviewSnapshot: &storage.ReviewSnapshot{Version: 1, RepositoryHead: run.StartingHead, RepositoryStatusSHA256: strings.Repeat("a", 64), RepositoryRefsSHA256: strings.Repeat("b", 64), Targets: cp.Checkpoint.Targets, Commits: []storage.ReviewCommit{{Reference: target.Reference.String(), WorkspaceID: e.WorkspaceID, StartingHead: e.StartingHead, ResultCommit: e.ResultCommit, Files: []string{"source.go"}}}}, ReviewResult: &storage.ReviewResult{Version: 1, Status: status, Summary: status, Findings: findings}}, "")
	if _, err = db.BeginCheckpointTriage(ctx, run.ID, run.Revision); err != nil {
		t.Fatal(err)
	}
	return &f, repo, db, run
}

func triageFinding(n int) storage.ReviewFinding {
	return storage.ReviewFinding{Title: fmt.Sprintf("Fix issue %d", n), Body: fmt.Sprintf("Trigger %d causes an incorrect result", n), Priority: 1, File: "source.go", Line: n}
}
func validAssessment(f storage.ReviewFinding) storage.FindingAssessment {
	return storage.FindingAssessment{FindingID: storage.ReviewFindingID(f), Decision: "valid", Reason: "Verified the failing branch in source.go against AGENTS.md.", Title: f.Title, Context: f.Body, Acceptance: "Add a regression for this trigger and restore the expected result.", ThreadID: "triage-thread", TurnID: "triage-turn"}
}
func reconcileFinding(t *testing.T, db *ProjectDatabase, run storage.ExecutionRun, a storage.FindingAssessment) storage.FindingAssessment {
	t.Helper()
	_, digest, err := db.CheckpointTriageQueue(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err = db.ReconcileCheckpointFinding(context.Background(), run.ID, a, digest)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCheckpointTriageDuplicatesExistingTasksAndDeterministicPlacement(t *testing.T) {
	a, b, c, d, e := triageFinding(1), triageFinding(2), triageFinding(3), triageFinding(4), triageFinding(5)
	f, repo, db, run := triageFixture(t, []storage.ReviewFinding{a, a, b, c, d, e})
	existing := addOrdinary(t, repo, f.main, "Existing independently owned issue")
	transitionReviewTest(t, repo, f.linked, existing, storage.PelletStart)
	tail := addOrdinary(t, repo, f.main, "unrelated tail")
	first := reconcileFinding(t, db, run, validAssessment(a))
	duplicate := validAssessment(b)
	duplicate.Decision = "duplicate"
	duplicate.DuplicateOf = first.FindingID
	dup := reconcileFinding(t, db, run, duplicate)
	if dup.PelletNumber != first.PelletNumber {
		t.Fatal("semantic duplicate allocated another Pellet")
	}
	covered := validAssessment(c)
	covered.Decision = "existing"
	covered.ExistingNumber = existing.Reference.Number
	got := reconcileFinding(t, db, run, covered)
	if got.PelletNumber != existing.Reference.Number {
		t.Fatal("existing issue not reconciled")
	}
	ignored := validAssessment(d)
	ignored.Decision = "already_fixed"
	reconcileFinding(t, db, run, ignored)
	second := reconcileFinding(t, db, run, validAssessment(e))
	queue, err := repo.ListPellets(context.Background(), f.main, storage.PelletListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var positions = map[int64]int{}
	for i, p := range queue {
		positions[p.Reference.Number] = i
		if p.Reference.Number == first.PelletNumber || p.Reference.Number == second.PelletNumber {
			if p.Kind != domain.PelletOrdinary || p.Group == nil || *p.Group != "Group/Exact" || p.ExternalID == nil || *p.ExternalID != "Issue:Exact" || !strings.Contains(p.Description, "Acceptance criteria:") {
				t.Fatalf("follow-up scope/context lost: %+v", p)
			}
		}
	}
	if positions[first.PelletNumber] != positions[run.PelletNumber]+1 || positions[second.PelletNumber] != positions[first.PelletNumber]+1 || positions[tail.Reference.Number] < positions[second.PelletNumber] {
		t.Fatalf("nondeterministic insertion: %+v", positions)
	}
	completed, err := db.CompleteReviewCheckpoint(context.Background(), run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || completed.ResultCommit != "" || len(completed.CheckpointTriage.Assessments) != 5 {
		t.Fatalf("completion: %+v", completed)
	}
}

func TestCheckpointTriageAtomicWritesLostResponsesExpiredRequestsAndResume(t *testing.T) {
	ctx := context.Background()
	a, b := triageFinding(1), triageFinding(2)
	f, repo, db, run := triageFixture(t, []storage.ReviewFinding{a, b})
	first := reconcileFinding(t, db, run, validAssessment(a))
	// Discarding this first response models a committed add with a lost reply.
	if _, err := repo.TransitionPellet(ctx, f.main, domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber}, storage.PelletLifecycleRequest{Operation: storage.PelletClose}); err == nil {
		t.Fatal("CLI lifecycle bypassed unfinished triage")
	}
	replay := reconcileFinding(t, db, run, validAssessment(a))
	if replay.PelletNumber != first.PelletNumber {
		t.Fatal("lost-response replay duplicated")
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE pellet_add_requests SET created_at=created_at-3`); err != nil {
		t.Fatal(err)
	}
	addOrdinary(t, repo, f.main, "prune expired request receipts")
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM pellet_add_requests`, 0)
	replay = reconcileFinding(t, db, run, validAssessment(a))
	if replay.PelletNumber != first.PelletNumber {
		t.Fatal("expired receipt duplicated")
	}
	// A failure after ordinary add but before the permanent receipt rolls BOTH
	// back, preserving the already durable first finding.
	if _, err := db.db.ExecContext(ctx, `CREATE TRIGGER reject_triage_receipt BEFORE INSERT ON checkpoint_finding_assessments WHEN NEW.ordinal=1 BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, digest, err := db.CheckpointTriageQueue(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ReconcileCheckpointFinding(ctx, run.ID, validAssessment(b), digest); err == nil {
		t.Fatal("injected partial write succeeded")
	}
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM checkpoint_finding_assessments`, 1)
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM pellets`, 4)
	if _, err = db.CompleteReviewCheckpoint(ctx, run.ID, run.Revision); err == nil {
		t.Fatal("partial triage closed checkpoint")
	}
	if _, err = db.db.ExecContext(ctx, `DROP TRIGGER reject_triage_receipt`); err != nil {
		t.Fatal(err)
	}
	stopped, err := db.InterruptExecutionRun(ctx, run.ID, run.Revision, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	capture := run.RunCapture
	capture.ResumeFrom = &stopped.ID
	resumed, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.CheckpointTriage == nil || len(resumed.CheckpointTriage.Assessments) != 1 || resumed.CheckpointTriage.Assessments[0].PelletNumber != first.PelletNumber {
		t.Fatal("resume lost partial results")
	}
	reconcileFinding(t, db, resumed, validAssessment(b))
	if _, err = db.CompleteReviewCheckpoint(ctx, resumed.ID, resumed.Revision); err != nil {
		t.Fatal(err)
	}
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM pellets`, 5)
}

func TestCheckpointTriageQueueRaceAndClosureFailure(t *testing.T) {
	ctx := context.Background()
	finding := triageFinding(1)
	f, repo, db, run := triageFixture(t, []storage.ReviewFinding{finding})
	_, digest, err := db.CheckpointTriageQueue(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	addOrdinary(t, repo, f.main, "newly queued issue")
	_, err = db.ReconcileCheckpointFinding(ctx, run.ID, validAssessment(finding), digest)
	assertDomainErrorCode(t, err, "triage_queue_changed")
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM checkpoint_finding_assessments`, 0)
	result := reconcileFinding(t, db, run, validAssessment(finding))
	if _, err = db.db.ExecContext(ctx, `CREATE TRIGGER reject_checkpoint_close BEFORE UPDATE OF status ON pellets WHEN NEW.kind='review_checkpoint' AND NEW.status='closed' BEGIN SELECT RAISE(ABORT,'injected close failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CompleteReviewCheckpoint(ctx, run.ID, run.Revision); err == nil {
		t.Fatal("injected close failure succeeded")
	}
	saved, err := db.ReadExecutionRun(ctx, run.ID)
	if err != nil || saved.State != "running" || saved.CheckpointTriage.Assessments[0].PelletNumber != result.PelletNumber {
		t.Fatalf("closure failure lost results: %+v %v", saved, err)
	}
	if _, err = db.db.ExecContext(ctx, `DROP TRIGGER reject_checkpoint_close`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CompleteReviewCheckpoint(ctx, run.ID, run.Revision); err != nil {
		t.Fatal(err)
	}
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM checkpoint_finding_assessments`, 1)
}

func TestCheckpointTriageNoFindingsClosesWithoutFollowups(t *testing.T) {
	_, _, db, run := triageFixture(t, []storage.ReviewFinding{})
	completed, err := db.CompleteReviewCheckpoint(context.Background(), run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "completed" || completed.ResultCommit != "" {
		t.Fatal("clean checkpoint did not complete without a commit")
	}
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM checkpoint_finding_assessments`, 0)
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM pellets`, 2)
}

func TestCheckpointTriageResumePlacesAfterSurvivingActiveFollowup(t *testing.T) {
	for _, retirement := range []string{"closed", "deferred", "purged"} {
		for _, keepEarlier := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/earlier_active=%t", retirement, keepEarlier), func(t *testing.T) {
				ctx := context.Background()
				a, b, c := triageFinding(1), triageFinding(2), triageFinding(3)
				f, repo, db, run := triageFixture(t, []storage.ReviewFinding{a, b, c})
				first := reconcileFinding(t, db, run, validAssessment(a))
				second := reconcileFinding(t, db, run, validAssessment(b))
				tail := addOrdinary(t, repo, f.main, "unrelated later work")
				stopped, err := db.InterruptExecutionRun(ctx, run.ID, run.Revision, "unknown")
				if err != nil {
					t.Fatal(err)
				}
				prior := append([]storage.FindingAssessment(nil), stopped.CheckpointTriage.Assessments...)
				retired := []int64{second.PelletNumber}
				if !keepEarlier {
					retired = append(retired, first.PelletNumber)
				} else {
					// A follow-up in progress in another workspace is still a
					// valid placement anchor, just like an open follow-up.
					ref := domain.PelletReference{ProjectCode: run.ProjectCode, Number: first.PelletNumber}
					if _, err = repo.TransitionPellet(ctx, f.linked, ref, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
						t.Fatal(err)
					}
				}
				for _, number := range retired {
					operation := storage.PelletClose
					if retirement == "deferred" {
						operation = storage.PelletDefer
					}
					ref := domain.PelletReference{ProjectCode: run.ProjectCode, Number: number}
					if _, err = repo.TransitionPellet(ctx, f.main, ref, storage.PelletLifecycleRequest{Operation: operation}); err != nil {
						t.Fatal(err)
					}
					if retirement == "purged" {
						// Age only these disposable fixture rows so the normal
						// date-filtered purge retains the reviewed target.
						if _, err = db.db.ExecContext(ctx, `UPDATE pellets SET created_at=created_at-3,updated_at=updated_at-3,completed_at=completed_at-3 WHERE project_id=? AND number=?`, run.ProjectID, number); err != nil {
							t.Fatal(err)
						}
					}
				}
				if retirement == "purged" {
					cutoff := time.Now().Add(-24 * time.Hour)
					removed, err := repo.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{CompletedBefore: &cutoff})
					if err != nil || len(removed) != len(retired) {
						t.Fatalf("purge: %+v %v", removed, err)
					}
				}
				capture := run.RunCapture
				capture.ResumeFrom = &stopped.ID
				resumed, err := db.CreateExecutionRun(ctx, capture)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(resumed.CheckpointTriage.Assessments, prior) {
					t.Fatal("resume changed durable receipts for retired follow-ups")
				}
				third := reconcileFinding(t, db, resumed, validAssessment(c))
				queue, err := repo.ListPellets(ctx, f.main, storage.PelletListOptions{})
				if err != nil {
					t.Fatal(err)
				}
				positions := map[int64]int{}
				for i, p := range queue {
					positions[p.Reference.Number] = i
				}
				anchor := run.PelletNumber
				if keepEarlier {
					anchor = first.PelletNumber
					if positions[anchor] != positions[run.PelletNumber]+1 {
						t.Fatalf("surviving follow-up moved: %+v", positions)
					}
				}
				if positions[third.PelletNumber] != positions[anchor]+1 || positions[tail.Reference.Number] != positions[third.PelletNumber]+1 {
					t.Fatalf("resumed insertion order: %+v", positions)
				}
				completed, err := db.CompleteReviewCheckpoint(ctx, resumed.ID, resumed.Revision)
				if err != nil {
					t.Fatal(err)
				}
				if completed.State != "completed" || len(completed.CheckpointTriage.Assessments) != 3 || !reflect.DeepEqual(completed.CheckpointTriage.Assessments[:2], prior) {
					t.Fatalf("completion lost prior results: %+v", completed.CheckpointTriage)
				}
			})
		}
	}
}
