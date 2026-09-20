package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"pellets/internal/storage"
)

func TestCheckpointCapturesExactImplementationContexts(t *testing.T) {
	ctx := context.Background()
	f, q, groups := groupFixture(t)
	db, err := OpenExecutionRunDatabase(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	g, err := groups.CreateGroupWithContext(ctx, f.main.Project, "shared", "# First revision\r\n")
	if err != nil {
		t.Fatal(err)
	}
	empty := mustGroup(t, groups, f.main.Project, "empty")
	other, err := groups.CreateGroupWithContext(ctx, f.main.Project, "other", "# Other group")
	if err != nil {
		t.Fatal(err)
	}
	var targets []storage.Pellet
	var implementations []storage.ExecutionRun
	for i, membership := range []*string{&g.Name, &g.Name, &other.Name, &empty.Name, nil, &g.Name} {
		if i == 1 {
			g, err = groups.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "# Second revision")
			if err != nil {
				t.Fatal(err)
			}
		}
		p, err := q.CreatePellet(ctx, f.main, storage.NewPellet{Title: "target", Group: membership})
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, p)
		implementations = append(implementations, completeReviewTarget(t, q, db, f.main, p))
	}
	// Model the migration's explicit legacy marker on one historical execution.
	mustExec(t, db.db, "DROP TRIGGER execution_group_context_immutable")
	if _, err := db.db.Exec(`UPDATE execution_runs SET group_context_json='{"version":1,"state":"legacy","group":null}' WHERE run_id=?`, implementations[5].ID); err != nil {
		t.Fatal(err)
	}
	implementations[5].GroupContext = storage.GroupContextSnapshot{Version: 1, State: "legacy"}
	checkpoint := addReview(t, q, f.main, targets...)
	checkpoint = transitionReviewTest(t, q, f.main, checkpoint, storage.PelletStart)
	capture := runCapture(f.main.Project.ID, f.main.Workspace.ID, checkpoint.Reference.Number)
	capture.Mode = "review_checkpoint"
	run, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &storage.ReviewSnapshot{Version: 2, RepositoryHead: run.StartingHead, RepositoryStatusSHA256: strings.Repeat("a", 64), RepositoryRefsSHA256: strings.Repeat("b", 64), Targets: checkpoint.Checkpoint.Targets}
	for i, target := range snapshot.Targets {
		context, err := db.ReadReviewTargetGroupContext(ctx, target)
		if err != nil || !reflect.DeepEqual(context, implementations[i].GroupContext) {
			t.Fatalf("target %d context = %+v, %v", i, context, err)
		}
		e := target.Evidence
		snapshot.Commits = append(snapshot.Commits, storage.ReviewCommit{Reference: target.Reference, WorkspaceID: e.WorkspaceID, StartingHead: e.StartingHead, ResultCommit: e.ResultCommit, Files: []string{"source.go"}})
		snapshot.GroupContexts = append(snapshot.GroupContexts, storage.ReviewGroupContext{ProjectID: target.ProjectID, Number: target.Number, RunID: e.RunID, Snapshot: context})
	}
	progress := storage.RunProgress{Phase: "review", State: "running", ThreadID: "review-thread", TurnID: "review-turn", ReviewSnapshot: snapshot}
	originalContext := snapshot.GroupContexts[0].Snapshot.Group.Context
	snapshot.GroupContexts[0].Snapshot.Group.Context = "invented requirements"
	if _, err := db.UpdateExecutionRun(ctx, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress}); err == nil {
		t.Fatal("initial save accepted context from outside the evidenced execution")
	}
	snapshot.GroupContexts[0].Snapshot.Group.Context = originalContext
	run = updateRun(t, db, run, progress, "")
	for _, change := range []func(*storage.ReviewTarget){
		func(target *storage.ReviewTarget) { target.Evidence.RunID = implementations[1].ID },
		func(target *storage.ReviewTarget) { target.ProjectID = f.other.Project.ID },
		func(target *storage.ReviewTarget) { target.Number++ },
		func(target *storage.ReviewTarget) { target.Evidence.WorkspaceID = f.linked.Workspace.ID },
		func(target *storage.ReviewTarget) { target.Description = "changed description" },
	} {
		target := snapshot.Targets[0]
		evidence := *target.Evidence
		target.Evidence = &evidence
		change(&target)
		if _, err := db.ReadReviewTargetGroupContext(ctx, target); err == nil {
			t.Fatal("context lookup ignored exact implementation identity")
		}
	}
	if _, err := groups.EditGroupContext(ctx, f.main.Project, g.ID, g.Revision, "new LIVE document"); err != nil {
		t.Fatal(err)
	}
	if _, err := groups.RenameGroup(ctx, f.main.Project, other.ID, other.Revision, "renamed"); err != nil {
		t.Fatal(err)
	}
	read, err := db.ReadExecutionRun(ctx, run.ID)
	if err != nil || !reflect.DeepEqual(snapshot, read.ReviewSnapshot) {
		t.Fatalf("live edits changed evidence: %v", err)
	}
	current, err := q.ReadPellet(ctx, f.main, checkpoint.Reference)
	if err != nil || current.Checkpoint.Ready {
		t.Fatalf("rename bypassed authoritative readiness: %+v %v", current.Checkpoint, err)
	}
	for i, target := range snapshot.Targets {
		got, err := db.ReadReviewTargetGroupContext(ctx, target)
		if err != nil || !reflect.DeepEqual(got, implementations[i].GroupContext) {
			t.Fatalf("live substitution for target %d: %+v %v", i, got, err)
		}
	}
	// Valid JSON is not sufficient: identities, exact source bytes, required
	// documents, and bounds are checked again when durable evidence is read.
	original, _ := json.Marshal(snapshot)
	var oversized storage.ReviewSnapshot
	if err := json.Unmarshal(original, &oversized); err != nil {
		t.Fatal(err)
	}
	oversized.GroupContexts[0].Snapshot.Group.Context = strings.Repeat("x", storage.MaxGroupContextBytes)
	if storage.ValidateGroupContextSnapshot(oversized.GroupContexts[0].Snapshot) != nil || storage.ValidateReviewSnapshot(&oversized) == nil {
		t.Fatal("aggregate review bound must reject oversized evidence without truncating valid documents")
	}
	for _, test := range []struct {
		name   string
		change func(*storage.ReviewSnapshot)
	}{
		{"missing", func(s *storage.ReviewSnapshot) { s.GroupContexts = nil }},
		{"wrong run", func(s *storage.ReviewSnapshot) { s.GroupContexts[0].RunID++ }},
		{"wrong target", func(s *storage.ReviewSnapshot) { s.GroupContexts[0].Number++ }},
		{"swapped revisions", func(s *storage.ReviewSnapshot) {
			s.GroupContexts[0].Snapshot, s.GroupContexts[1].Snapshot = s.GroupContexts[1].Snapshot, s.GroupContexts[0].Snapshot
		}},
		{"changed document", func(s *storage.ReviewSnapshot) { s.GroupContexts[0].Snapshot.Group.Context = "live substitute" }},
		{"invalid state", func(s *storage.ReviewSnapshot) { s.GroupContexts[0].Snapshot.State = "missing" }},
		{"legacy with context", func(s *storage.ReviewSnapshot) { s.Version = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var changed storage.ReviewSnapshot
			if err := json.Unmarshal(original, &changed); err != nil {
				t.Fatal(err)
			}
			test.change(&changed)
			encoded, _ := json.Marshal(changed)
			if _, err := db.db.Exec(`UPDATE execution_runs SET review_snapshot_json=? WHERE run_id=?`, string(encoded), run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ReadExecutionRun(ctx, run.ID); err == nil {
				t.Fatal("corrupt evidence accepted")
			}
		})
	}
	if _, err := db.db.Exec(`UPDATE execution_runs SET review_snapshot_json=? WHERE run_id=?`, string(original), run.ID); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []string{`null`, `{}`, `{"version":1,"state":"captured","group":null}`, `{"version":99,"state":"legacy","group":null}`,
		`{"version":1,"state":"captured","group":{"id":1,"name":"shared","revision":1}}`,
		`{"version":1,"state":"captured","group":{"id":1,"name":"shared","revision":1,"context":null}}`,
		`{"version":1,"state":"legacy"}`} {
		if _, err := db.db.Exec(`UPDATE execution_runs SET group_context_json=? WHERE run_id=?`, corrupt, implementations[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ReadReviewTargetGroupContext(ctx, snapshot.Targets[0]); err == nil {
			t.Fatal("missing implementation context accepted")
		}
		if _, err := db.ReadExecutionRun(ctx, run.ID); err == nil {
			t.Fatal("review ignored corrupt implementation evidence")
		}
	}
}

func TestCheckpointContextDigestAndLegacyTriage(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			ctx := context.Background()
			_, _, db, run := triageFixture(t, []storage.ReviewFinding{triageFinding(1)})
			// Upgrade this fixture before beginning v2 triage; its original v1 receipt
			// is explicitly removed only to simulate pre-receipt review completion.
			mustExec(t, db.db, `DELETE FROM checkpoint_triage`)
			run.ReviewSnapshot.Version = version
			if version == 2 {
				target := run.ReviewSnapshot.Targets[0]
				context, err := db.ReadReviewTargetGroupContext(ctx, target)
				if err != nil {
					t.Fatal(err)
				}
				run.ReviewSnapshot.GroupContexts = []storage.ReviewGroupContext{{ProjectID: target.ProjectID, Number: target.Number, RunID: target.Evidence.RunID, Snapshot: context}}
			}
			encoded, _ := json.Marshal(run.ReviewSnapshot)
			if _, err := db.db.Exec(`UPDATE execution_runs SET review_snapshot_json=? WHERE run_id=?`, string(encoded), run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.BeginCheckpointTriage(ctx, run.ID, run.Revision); err != nil {
				t.Fatal(err)
			}
			first := reconcileFinding(t, db, run, validAssessment(triageFinding(1)))
			// Even structurally valid replacement evidence must not inherit the
			// completed assessment and its permanent follow-up receipt.
			run.ReviewSnapshot.RepositoryStatusSHA256 = strings.Repeat("c", 64)
			changed, _ := json.Marshal(run.ReviewSnapshot)
			if _, err := db.db.Exec(`UPDATE execution_runs SET review_snapshot_json=? WHERE run_id=?`, string(changed), run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ReadCheckpointTriage(ctx, run.ID); err == nil {
				t.Fatal("changed snapshot reused completed triage")
			}
			if _, err := db.db.Exec(`UPDATE execution_runs SET review_snapshot_json=? WHERE run_id=?`, string(encoded), run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.Exec(`UPDATE checkpoint_triage SET review_snapshot_sha256=''`); err == nil {
				t.Fatal("digest is mutable")
			}
			mustExec(t, db.db, `DROP TRIGGER checkpoint_triage_snapshot_immutable`)
			mustExec(t, db.db, `UPDATE checkpoint_triage SET review_snapshot_sha256=''`)
			got, err := db.ReadCheckpointTriage(ctx, run.ID)
			if version == 1 {
				if err != nil || len(got.Assessments) != 1 || got.Assessments[0].PelletNumber != first.PelletNumber {
					t.Fatalf("legacy receipt lost: %+v %v", got, err)
				}
				if err := json.Unmarshal(encoded, &run.ReviewSnapshot); err != nil {
					t.Fatal(err)
				}
				progress := run.RunProgress
				progress.State, progress.Outcome = "interrupted", "unknown"
				run = updateRun(t, db, run, progress, "")
				capture := run.RunCapture
				capture.ResumeFrom = &run.ID
				resumed, err := db.CreateExecutionRun(ctx, capture)
				if err != nil || !reflect.DeepEqual(resumed.ReviewSnapshot, run.ReviewSnapshot) || len(resumed.ReviewSnapshot.GroupContexts) != 0 {
					t.Fatalf("legacy Resume inferred context: %+v %v", resumed.ReviewSnapshot, err)
				}
				if replay := reconcileFinding(t, db, resumed, validAssessment(triageFinding(1))); replay.PelletNumber != first.PelletNumber {
					t.Fatal("legacy Resume duplicated a permanent finding receipt")
				}
			} else if err == nil {
				t.Fatal("missing v2 digest accepted")
			}
		})
	}
}
