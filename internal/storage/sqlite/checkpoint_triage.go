package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func triageRun(ctx context.Context, q runQuery, id int64) (storage.ExecutionRun, error) {
	r, err := readExecutionRun(ctx, q, id)
	if err != nil {
		return r, err
	}
	if r.Mode != "review_checkpoint" || r.ReviewResult == nil || r.ReviewSnapshot == nil || r.PendingOperation != "" {
		return r, storage.ExecutionRunConflict(id)
	}
	return r, nil
}

func readCheckpointTriage(ctx context.Context, q runQuery, r storage.ExecutionRun) (*storage.CheckpointTriage, error) {
	var encoded string
	t := &storage.CheckpointTriage{Assessments: []storage.FindingAssessment{}}
	err := q.QueryRowContext(ctx, `SELECT review_result_json,review_thread_id,review_turn_id FROM checkpoint_triage WHERE project_id=? AND checkpoint_number=? AND implementation_revision=?`, r.ProjectID, r.PelletNumber, r.ImplementationRevision).Scan(&encoded, &t.ReviewThreadID, &t.ReviewTurnID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	want, _ := json.Marshal(r.ReviewResult)
	if encoded != string(want) || t.ReviewThreadID != r.ThreadID || t.ReviewTurnID != r.TurnID {
		return nil, storage.ExecutionRunConflict(r.ID)
	}
	rows, err := q.QueryContext(ctx, `SELECT assessment_json FROM checkpoint_finding_assessments WHERE project_id=? AND checkpoint_number=? AND implementation_revision=? ORDER BY ordinal`, r.ProjectID, r.PelletNumber, r.ImplementationRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var a storage.FindingAssessment
		if err = rows.Scan(&s); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(s), &a); err != nil {
			return nil, err
		}
		t.Assessments = append(t.Assessments, a)
	}
	return t, rows.Err()
}

func (db *ProjectDatabase) ReadCheckpointTriage(ctx context.Context, id int64) (*storage.CheckpointTriage, error) {
	r, err := triageRun(ctx, db.db, id)
	if err != nil {
		return nil, err
	}
	return readCheckpointTriage(ctx, db.db, r)
}

// Only the driver calls this after observing the exact successful review turn.
func (db *ProjectDatabase) BeginCheckpointTriage(ctx context.Context, id, revision int64) (triage *storage.CheckpointTriage, err error) {
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		r, e := triageRun(ctx, conn, id)
		if e != nil {
			return e
		}
		if r.Revision != revision || !storage.RunActive(r.State) {
			return storage.ExecutionRunConflict(id)
		}
		triage, e = readCheckpointTriage(ctx, conn, r)
		if e != nil || triage != nil {
			return e
		}
		encoded, _ := json.Marshal(r.ReviewResult)
		_, e = conn.ExecContext(ctx, `INSERT INTO checkpoint_triage(project_id,checkpoint_number,implementation_revision,review_result_json,review_thread_id,review_turn_id) VALUES(?,?,?,?,?,?)`, r.ProjectID, r.PelletNumber, r.ImplementationRevision, string(encoded), r.ThreadID, r.TurnID)
		if e != nil {
			return e
		}
		triage = &storage.CheckpointTriage{ReviewThreadID: r.ThreadID, ReviewTurnID: r.TurnID, Assessments: []storage.FindingAssessment{}}
		return nil
	})
	return
}

func triageQueue(ctx context.Context, q runQuery, r storage.ExecutionRun) ([]storage.Pellet, string, error) {
	rows, err := q.QueryContext(ctx, pelletSelect+` WHERE p.project_id=? AND p.status IN ('open','in_progress') ORDER BY p.number`, r.ProjectID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	queue := []storage.Pellet{}
	for rows.Next() {
		p, e := scanPellet(rows)
		if e != nil {
			return nil, "", e
		}
		queue = append(queue, p)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	b, err := json.Marshal(queue)
	if err != nil {
		return nil, "", err
	}
	if len(b) > storage.MaxRunSnapshotBytes {
		return nil, "", storage.InvalidExecutionRun("the active queue exceeds the bounded triage context; it cannot be silently truncated")
	}
	return queue, fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

func (db *ProjectDatabase) CheckpointTriageQueue(ctx context.Context, id int64) ([]storage.Pellet, string, error) {
	r, err := triageRun(ctx, db.db, id)
	if err != nil {
		return nil, "", err
	}
	return triageQueue(ctx, db.db, r)
}

// The permanent receipt and ordinary add (including its short-lived request
// receipt) share one writer transaction. Lost replies and expired request IDs
// therefore cannot allocate a second Pellet, even across explicit resumes.
func (db *ProjectDatabase) ReconcileCheckpointFinding(ctx context.Context, id int64, a storage.FindingAssessment, queueDigest string) (result storage.FindingAssessment, err error) {
	if err = storage.ValidateFindingAssessment(a); err != nil {
		return
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		r, e := triageRun(ctx, conn, id)
		if e != nil {
			return e
		}
		t, e := readCheckpointTriage(ctx, conn, r)
		if e != nil {
			return e
		}
		if t == nil {
			return storage.InvalidExecutionRun("the reviewer has no successful terminal receipt")
		}
		for _, saved := range t.Assessments {
			if saved.FindingID == a.FindingID {
				input := a
				input.PelletNumber = saved.PelletNumber
				if !reflect.DeepEqual(input, saved) {
					return storage.ExecutionRunConflict(id)
				}
				result = saved
				return nil
			}
		}
		if !storage.RunActive(r.State) {
			return storage.ExecutionRunConflict(id)
		}
		checkpoint, e := loadPellet(ctx, conn, r.ProjectID, r.PelletNumber)
		if e != nil {
			return e
		}
		if checkpoint.Status != domain.PelletInProgress || checkpoint.Workspace == nil || checkpoint.Workspace.ID != r.WorkspaceID || checkpoint.ImplementationRevision != r.ImplementationRevision || !storage.SameReviewScope(checkpoint.Checkpoint, r.CheckpointScope) {
			return storage.ExecutionRunConflict(id)
		}
		if e = requireCheckpointReady(checkpoint); e != nil {
			return e
		}
		seen := map[string]bool{}
		ordinal := -1
		for _, f := range r.ReviewResult.Findings {
			key := storage.ReviewFindingID(f)
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(seen)-1 == len(t.Assessments) {
				if key == a.FindingID {
					ordinal = len(t.Assessments)
				}
				break
			}
		}
		if ordinal < 0 {
			return storage.InvalidExecutionRun("triage must reconcile every distinct finding in recorded order")
		}
		queue, digest, e := triageQueue(ctx, conn, r)
		if e != nil {
			return e
		}
		if digest != queueDigest {
			return domain.NewError(domain.Conflict, "triage_queue_changed", "the active queue changed during assessment; reassess this finding against the current queue", nil)
		}
		if a.PelletNumber != 0 {
			return storage.InvalidExecutionRun("triage output cannot assign a new Pellet number")
		}
		switch a.Decision {
		case "existing":
			found := false
			for _, p := range queue {
				if p.Reference.Number == a.ExistingNumber && p.Kind == domain.PelletOrdinary {
					found = true
					a.PelletNumber = p.Reference.Number
				}
			}
			if !found {
				return storage.InvalidExecutionRun("existing finding must match a current ordinary open or in-progress Pellet")
			}
		case "duplicate":
			found := false
			for _, prior := range t.Assessments {
				if prior.FindingID == a.DuplicateOf {
					found = true
					a.PelletNumber = prior.PelletNumber
				}
			}
			if !found {
				return storage.InvalidExecutionRun("duplicate finding must name an earlier reconciled finding")
			}
		case "valid":
			anchor := checkpoint.Reference
			active := make(map[int64]bool, len(queue))
			for _, p := range queue {
				active[p.Reference.Number] = true
			}
			// Receipts survive closure, deferral, and purge. Resolve placement
			// against this transaction's current active queue, preserving the
			// last surviving created follow-up in recorded finding order.
			for _, prior := range t.Assessments {
				if prior.Decision == "valid" && active[prior.PelletNumber] {
					anchor.Number = prior.PelletNumber
				}
			}
			requestID := fmt.Sprintf("checkpoint:%d:%d:%d:%s", r.ProjectID, r.PelletNumber, r.ImplementationRevision, a.FindingID)
			input := storage.NewPellet{RequestID: &requestID, Title: a.Title, Description: fmt.Sprintf("Review checkpoint %s, finding %s.\n\n%s\n\nAssessment: %s\n\nAcceptance criteria:\n%s", checkpoint.Reference.String(), a.FindingID, a.Context, a.Reason, a.Acceptance), Group: checkpoint.Group, ExternalID: checkpoint.ExternalID, Placement: &storage.PelletPlacement{Target: anchor}}
			normalized, e := validateNewPellet(input)
			if e != nil {
				return e
			}
			project := storage.ResolvedProject{Project: storage.Project{ID: r.ProjectID, Code: r.ProjectCode}}
			p, e := createPelletInTransaction(ctx, conn, project, normalized)
			if e != nil {
				return e
			}
			a.PelletNumber = p.Reference.Number
		}
		encoded, e := json.Marshal(a)
		if e != nil {
			return e
		}
		_, e = conn.ExecContext(ctx, `INSERT INTO checkpoint_finding_assessments(project_id,checkpoint_number,implementation_revision,finding_id,ordinal,assessment_json) VALUES(?,?,?,?,?,?)`, r.ProjectID, r.PelletNumber, r.ImplementationRevision, a.FindingID, ordinal, string(encoded))
		if e != nil {
			return e
		}
		result = a
		return nil
	})
	return
}

func requireCompletedTriage(ctx context.Context, q runQuery, r storage.ExecutionRun) error {
	t, err := readCheckpointTriage(ctx, q, r)
	if err != nil {
		return err
	}
	// A clean native review has no model assessments to perform. Its successful
	// terminal event is checked by the application completion boundary.
	if r.ReviewResult.Status == "clean" {
		return nil
	}
	seen := map[string]bool{}
	for _, f := range r.ReviewResult.Findings {
		seen[storage.ReviewFindingID(f)] = true
	}
	if t == nil || len(t.Assessments) != len(seen) {
		return storage.InvalidExecutionRun("checkpoint cannot close with unfinished finding triage")
	}
	for _, a := range t.Assessments {
		if !seen[a.FindingID] {
			return storage.ExecutionRunConflict(r.ID)
		}
		if (a.Decision == "valid" || a.Decision == "existing") && a.PelletNumber < 1 {
			return storage.InvalidExecutionRun("checkpoint follow-up has no durable Pellet receipt")
		}
	}
	return nil
}
