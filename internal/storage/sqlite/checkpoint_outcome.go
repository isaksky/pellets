package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"

	"pellets/internal/storage"
)

// ReadCheckpointOutcome also serves historical generations in the inspector.
func (reader *WebReader) ReadCheckpointOutcome(ctx context.Context, projectID, number, generation int64) (storage.CheckpointOutcome, error) {
	outcomes, err := reader.ReadCheckpointOutcomes(ctx, []storage.CheckpointIdentity{{ProjectID: projectID, Number: number, ImplementationRevision: generation}})
	if err != nil {
		return storage.CheckpointOutcome{}, err
	}
	return outcomes[0], nil
}

const outcomeIdentitiesSQL = `WITH identities AS (
 SELECT CAST(key AS INTEGER) AS idx, json_extract(value,'$.ProjectID') AS project_id,
 json_extract(value,'$.Number') AS number, json_extract(value,'$.ImplementationRevision') AS generation
 FROM json_each(?)
) `

// ReadCheckpointOutcomes reads exact generations in two queries per bounded
// batch, in one snapshot. It uses permanent receipts, never bounded workspace
// history. Only allowlisted scalar evidence leaves this read-only boundary.
func (reader *WebReader) ReadCheckpointOutcomes(ctx context.Context, identities []storage.CheckpointIdentity) ([]storage.CheckpointOutcome, error) {
	outcomes := make([]storage.CheckpointOutcome, 0, len(identities))
	for _, id := range identities {
		if id.ProjectID < 1 || id.Number < 1 || id.ImplementationRevision < 1 {
			return nil, storage.InvalidExecutionRun("checkpoint identity and generation must be positive")
		}
	}
	if len(identities) == 0 {
		return outcomes, nil
	}
	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for start := 0; start < len(identities); start += 100 {
		end := min(start+100, len(identities))
		batch, err := readCheckpointOutcomeBatch(ctx, tx, identities[start:end])
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, batch...)
	}
	return outcomes, tx.Commit()
}

func readCheckpointOutcomeBatch(ctx context.Context, tx *sql.Tx, identities []storage.CheckpointIdentity) ([]storage.CheckpointOutcome, error) {
	encodedIDs, err := json.Marshal(identities)
	if err != nil {
		return nil, err
	}
	outcomes := make([]storage.CheckpointOutcome, len(identities))
	rows, err := tx.QueryContext(ctx, outcomeIdentitiesSQL+`
 SELECT i.idx, COALESCE(r.run_id,0), COALESCE(r.state,''), COALESCE(r.phase,''),
 COALESCE(t.review_result_json, (
 SELECT review_result_json FROM execution_runs e WHERE e.project_id=i.project_id AND e.pellet_number=i.number AND e.implementation_revision=i.generation
 AND e.mode='review_checkpoint' AND e.state='completed' AND e.outcome='succeeded' AND e.pending_operation='' AND e.thread_id<>'' AND e.turn_id<>''
 AND e.review_result_json<>'null' AND e.review_snapshot_json<>'null' AND e.checkpoint_scope_json<>'null' ORDER BY e.run_id DESC LIMIT 1), '')
 FROM identities i
 LEFT JOIN execution_runs r ON r.run_id=(SELECT run_id FROM execution_runs WHERE project_id=i.project_id AND pellet_number=i.number AND implementation_revision=i.generation AND mode='review_checkpoint' ORDER BY run_id DESC LIMIT 1)
 LEFT JOIN checkpoint_triage t ON t.project_id=i.project_id AND t.checkpoint_number=i.number AND t.implementation_revision=i.generation
 ORDER BY i.idx`, string(encodedIDs))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var idx int
		var o storage.CheckpointOutcome
		var encoded string
		if err = rows.Scan(&idx, &o.RunID, &o.RunState, &o.RunPhase, &encoded); err != nil {
			break
		}
		id := identities[idx]
		o.ProjectID, o.CheckpointNumber, o.ImplementationRevision = id.ProjectID, id.Number, id.ImplementationRevision
		o.Status, o.Triage = "pending", "not_started"
		if o.RunID != 0 {
			o.NeedsAttention = !storage.RunActive(o.RunState) && o.RunState != "completed" || o.RunState == "awaiting_input"
			o.Status = "running"
			if o.NeedsAttention {
				o.Status = "needs_attention"
			}
		}
		if encoded != "" {
			var result storage.ReviewResult
			if err = json.Unmarshal([]byte(encoded), &result); err != nil {
				break
			}
			if err = storage.ValidateReviewResult(&result); err != nil {
				break
			}
			o.ReviewCompleted, o.Status = true, result.Status
			seen := map[string]bool{}
			for _, finding := range result.Findings {
				seen[storage.ReviewFindingID(finding)] = true
			}
			o.Findings = len(seen)
		}
		outcomes[idx] = o
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	rows, err = tx.QueryContext(ctx, outcomeIdentitiesSQL+`
 SELECT i.idx,a.ordinal,json_extract(a.assessment_json,'$.decision'),COALESCE(json_extract(a.assessment_json,'$.pellet_number'),0),
 EXISTS(SELECT 1 FROM pellets p WHERE p.project_id=a.project_id AND p.number=json_extract(a.assessment_json,'$.pellet_number'))
 FROM identities i JOIN checkpoint_finding_assessments a ON a.project_id=i.project_id AND a.checkpoint_number=i.number AND a.implementation_revision=i.generation
 WHERE a.ordinal <= 1000 ORDER BY i.idx,a.ordinal`, string(encodedIDs))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var idx int
		var d storage.CheckpointDisposition
		if err = rows.Scan(&idx, &d.FindingNumber, &d.Decision, &d.PelletNumber, &d.PelletPresent); err != nil {
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
		if outcomes[idx].ReviewCompleted {
			outcomes[idx].Dispositions = append(outcomes[idx].Dispositions, d)
		}
	}
	rowErr = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	for i := range outcomes {
		o := &outcomes[i]
		if !o.ReviewCompleted {
			continue
		}
		o.Assessed = len(o.Dispositions)
		if o.Assessed > o.Findings {
			return nil, storage.InvalidExecutionRun("triage exceeds the recorded distinct findings")
		}
		o.Triage = "complete"
		if o.Assessed < o.Findings {
			o.Triage = "partial"
			if o.RunState == "completed" {
				o.NeedsAttention = true
			}
		}
	}
	return outcomes, nil
}
