package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestCheckpointOutcomeDurablePartialResumeAndExactGeneration(t *testing.T) {
	ctx := context.Background()
	a, b := triageFinding(1), triageFinding(2)
	a.Title, a.Body = "secret reviewer title", "secret reviewer transcript"
	f, repo, db, run := triageFixture(t, []storage.ReviewFinding{a, a, b})
	first := validAssessment(a)
	first.Reason = "secret assessment command output"
	created := reconcileFinding(t, db, run, first)
	read := func() storage.CheckpointOutcome {
		t.Helper()
		reader, err := OpenWebReader(ctx, f.path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		o, err := reader.ReadCheckpointOutcome(ctx, run.ProjectID, run.PelletNumber, run.ImplementationRevision)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(o)
		if strings.Contains(string(encoded), "secret") {
			t.Fatalf("raw review content escaped: %s", encoded)
		}
		return o
	}
	partial := read()
	if partial.Status != "findings" || partial.Findings != 2 || partial.Assessed != 1 || partial.Triage != "partial" || partial.Dispositions[0].PelletNumber != created.PelletNumber {
		t.Fatalf("partial: %+v", partial)
	}
	stopped, err := db.InterruptExecutionRun(ctx, run.ID, run.Revision, "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if o := read(); !o.NeedsAttention || o.Status != "findings" || o.Assessed != 1 {
		t.Fatalf("interrupted: %+v", o)
	}
	capture := run.RunCapture
	capture.ResumeFrom = &stopped.ID
	resumed, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	a2 := validAssessment(b)
	a2.Decision = "already_fixed"
	reconcileFinding(t, db, resumed, a2)
	if _, err = db.CompleteReviewCheckpoint(ctx, resumed.ID, resumed.Revision); err != nil {
		t.Fatal(err)
	}
	complete := read()
	if complete.RunID != resumed.ID || complete.NeedsAttention || complete.Triage != "complete" || complete.Assessed != 2 || complete.Dispositions[1].Decision != "already_fixed" {
		t.Fatalf("recovered: %+v", complete)
	}
	// More ordinary runs than the dashboard retains cannot erase this receipt.
	for i := 0; i < 10; i++ {
		completeReviewTarget(t, repo, db, f.main, addOrdinary(t, repo, f.main, "later ordinary run"))
	}
	if got := read(); !reflect.DeepEqual(got, complete) {
		t.Fatalf("later runs changed checkpoint outcome: %+v", got)
	}
	reader, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, identity := range [][3]int64{{run.ProjectID, run.PelletNumber, run.ImplementationRevision + 1}, {run.ProjectID, run.PelletNumber + 1, run.ImplementationRevision}, {f.other.Project.ID, run.PelletNumber, run.ImplementationRevision}} {
		o, err := reader.ReadCheckpointOutcome(ctx, identity[0], identity[1], identity[2])
		if err != nil || o.ReviewCompleted || o.Status != "pending" || len(o.Dispositions) != 0 {
			t.Fatalf("cross-identity leak: %+v %v", o, err)
		}
	}
}

func TestCheckpointOutcomePreservesPurgedFollowupReference(t *testing.T) {
	ctx := context.Background()
	finding := triageFinding(1)
	f, repo, db, run := triageFixture(t, []storage.ReviewFinding{finding})
	a := reconcileFinding(t, db, run, validAssessment(finding))
	ref := domain.PelletReference{ProjectCode: run.ProjectCode, Number: a.PelletNumber}
	if _, err := repo.TransitionPellet(ctx, f.main, ref, storage.PelletLifecycleRequest{Operation: storage.PelletClose}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PurgeClosedPellets(ctx, f.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	o, err := reader.ReadCheckpointOutcome(ctx, run.ProjectID, run.PelletNumber, run.ImplementationRevision)
	if err != nil || len(o.Dispositions) != 1 || o.Dispositions[0].PelletNumber != a.PelletNumber || o.Dispositions[0].PelletPresent {
		t.Fatalf("purge erased reference: %+v %v", o, err)
	}
	cp, err := reader.ReadWebPellet(ctx, f.main.Project, domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber})
	if err != nil || cp.Kind != domain.PelletReviewCheckpoint {
		t.Fatalf("checkpoint lost: %+v %v", cp, err)
	}
}

func TestCheckpointOutcomeCleanAndEveryNonCreationDisposition(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		f, _, db, run := triageFixture(t, nil)
		if _, err := db.CompleteReviewCheckpoint(context.Background(), run.ID, run.Revision); err != nil {
			t.Fatal(err)
		}
		reader, err := OpenWebReader(context.Background(), f.path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		o, err := reader.ReadCheckpointOutcome(context.Background(), run.ProjectID, run.PelletNumber, run.ImplementationRevision)
		if err != nil || o.Status != "clean" || o.Triage != "complete" || o.Findings != 0 || !o.ReviewCompleted {
			t.Fatalf("clean: %+v %v", o, err)
		}
	})
	t.Run("dispositions", func(t *testing.T) {
		findings := []storage.ReviewFinding{triageFinding(1), triageFinding(2), triageFinding(3), triageFinding(4), triageFinding(5), triageFinding(6)}
		f, repo, db, run := triageFixture(t, findings)
		existing := addOrdinary(t, repo, f.main, "existing")
		decisions := []string{"valid", "duplicate", "existing", "invalid", "already_fixed", "stylistic"}
		for i, decision := range decisions {
			a := validAssessment(findings[i])
			a.Decision = decision
			if decision == "duplicate" {
				a.DuplicateOf = storage.ReviewFindingID(findings[0])
			}
			if decision == "existing" {
				a.ExistingNumber = existing.Reference.Number
			}
			reconcileFinding(t, db, run, a)
		}
		reader, err := OpenWebReader(context.Background(), f.path)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		o, err := reader.ReadCheckpointOutcome(context.Background(), run.ProjectID, run.PelletNumber, run.ImplementationRevision)
		if err != nil || o.Assessed != len(decisions) {
			t.Fatalf("dispositions: %+v %v", o, err)
		}
		for i, d := range o.Dispositions {
			if d.FindingNumber != i+1 || d.Decision != decisions[i] {
				t.Fatalf("disposition: %+v", d)
			}
		}
	})
}

func TestCheckpointOutcomeCompletedBeforeTriageReceipts(t *testing.T) {
	for _, findings := range [][]storage.ReviewFinding{nil, {triageFinding(1)}} {
		t.Run(fmt.Sprintf("findings_%d", len(findings)), func(t *testing.T) {
			ctx := context.Background()
			f, _, db, run := triageFixture(t, findings)
			for _, finding := range findings {
				reconcileFinding(t, db, run, validAssessment(finding))
			}
			if _, err := db.CompleteReviewCheckpoint(ctx, run.ID, run.Revision); err != nil {
				t.Fatal(err)
			}
			// Model an upgrade from schema 13, which retained the successful
			// review run but had no triage tables or assessment receipts.
			if _, err := db.db.ExecContext(ctx, `DELETE FROM checkpoint_finding_assessments`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.ExecContext(ctx, `DELETE FROM checkpoint_triage`); err != nil {
				t.Fatal(err)
			}
			reader, err := OpenWebReader(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			o, err := reader.ReadCheckpointOutcome(ctx, run.ProjectID, run.PelletNumber, run.ImplementationRevision)
			if err != nil || !o.ReviewCompleted || o.Findings != len(findings) || o.Assessed != 0 {
				t.Fatalf("legacy completion: %+v %v", o, err)
			}
			if len(findings) == 0 && (o.Status != "clean" || o.Triage != "complete") {
				t.Fatalf("legacy clean review: %+v", o)
			}
			if len(findings) != 0 && (o.Status != "findings" || o.Triage != "partial" || !o.NeedsAttention) {
				t.Fatalf("legacy unreconciled findings: %+v", o)
			}
		})
	}
}
