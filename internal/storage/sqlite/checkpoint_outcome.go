package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"pellets/internal/storage"
)

// ReadCheckpointOutcome uses the permanent receipt and the exact generation's
// latest review attempt, never the workspace's bounded recent-run list. A read
// transaction keeps the receipt and its assessment counts coherent during
// reconciliation. No supervisor or live Codex connection is required.
func (reader *WebReader) ReadCheckpointOutcome(ctx context.Context, projectID, number, generation int64) (storage.CheckpointOutcome, error) {
	o := storage.CheckpointOutcome{ProjectID: projectID, CheckpointNumber: number, ImplementationRevision: generation, Status: "pending", Triage: "not_started"}
	if projectID < 1 || number < 1 || generation < 1 {
		return o, storage.InvalidExecutionRun("checkpoint identity and generation must be positive")
	}
	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return o, err
	}
	defer tx.Rollback()
	var state string
	err = tx.QueryRowContext(ctx, `SELECT run_id,state FROM execution_runs WHERE project_id=? AND pellet_number=? AND implementation_revision=? AND mode='review_checkpoint' ORDER BY run_id DESC LIMIT 1`, projectID, number, generation).Scan(&o.RunID, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return o, err
	}
	if o.RunID != 0 {
		o.NeedsAttention = !storage.RunActive(state) && state != "completed" || state == "awaiting_input"
		if o.NeedsAttention {
			o.Status = "needs_attention"
		} else {
			o.Status = "running"
		}
	}
	var encoded string
	err = tx.QueryRowContext(ctx, `SELECT review_result_json FROM checkpoint_triage WHERE project_id=? AND checkpoint_number=? AND implementation_revision=?`, projectID, number, generation).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		// Completed reviews existed before permanent triage receipts. Their
		// exact successful completion is still durable evidence after upgrade;
		// missing assessments must remain partial, never invented as complete.
		err = tx.QueryRowContext(ctx, `SELECT review_result_json FROM execution_runs WHERE project_id=? AND pellet_number=? AND implementation_revision=? AND mode='review_checkpoint' AND state='completed' AND outcome='succeeded' AND pending_operation='' AND thread_id<>'' AND turn_id<>'' AND review_result_json<>'null' AND review_snapshot_json<>'null' AND checkpoint_scope_json<>'null' ORDER BY run_id DESC LIMIT 1`, projectID, number, generation).Scan(&encoded)
		if errors.Is(err, sql.ErrNoRows) {
			return o, tx.Commit()
		}
	}
	if err != nil {
		return o, err
	}
	var result storage.ReviewResult
	if err = json.Unmarshal([]byte(encoded), &result); err != nil {
		return o, err
	}
	if err = storage.ValidateReviewResult(&result); err != nil {
		return o, err
	}
	o.ReviewCompleted, o.Status = true, result.Status
	seen := map[string]bool{}
	for _, finding := range result.Findings {
		seen[storage.ReviewFindingID(finding)] = true
	}
	o.Findings = len(seen)
	// Only allowlisted scalar fields leave storage. In particular, never load
	// the potentially large free-text assessment into the HTTP view.
	rows, err := tx.QueryContext(ctx, `SELECT ordinal,json_extract(assessment_json,'$.decision'),COALESCE(json_extract(assessment_json,'$.pellet_number'),0),EXISTS(SELECT 1 FROM pellets p WHERE p.project_id=a.project_id AND p.number=json_extract(a.assessment_json,'$.pellet_number')) FROM checkpoint_finding_assessments a WHERE project_id=? AND checkpoint_number=? AND implementation_revision=? ORDER BY ordinal LIMIT 1001`, projectID, number, generation)
	if err != nil {
		return o, err
	}
	for rows.Next() {
		var d storage.CheckpointDisposition
		if err = rows.Scan(&d.FindingNumber, &d.Decision, &d.PelletNumber, &d.PelletPresent); err != nil {
			break
		}
		d.FindingNumber++
		switch d.Decision {
		case "valid", "existing", "duplicate", "invalid", "already_fixed", "stylistic":
		default:
			err = storage.InvalidExecutionRun("unknown durable triage disposition")
		}
		if err != nil {
			break
		}
		o.Dispositions = append(o.Dispositions, d)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return o, err
	}
	if rowErr != nil {
		return o, rowErr
	}
	o.Assessed = len(o.Dispositions)
	if o.Assessed > o.Findings {
		return o, storage.InvalidExecutionRun("triage exceeds the recorded distinct findings")
	}
	o.Triage = "complete"
	if o.Assessed < o.Findings {
		o.Triage = "partial"
		if state == "completed" {
			o.NeedsAttention = true
		}
	}
	return o, tx.Commit()
}
