package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type changeQuery interface {
	runQuery
	projectQuery
}

func pendingExecutionChange(ctx context.Context, q changeQuery, id int64) (*storage.ExecutionChange, error) {
	run, err := readExecutionRun(ctx, q, id)
	if err != nil {
		return nil, err
	}
	p, err := loadPellet(ctx, q, run.ProjectID, run.PelletNumber)
	if err != nil {
		return nil, err
	}
	if p.ImplementationRevision == run.ImplementationRevision {
		return nil, nil
	}
	if !storage.RunActive(run.State) || run.Finalization != nil || run.Mode == "review_checkpoint" || p.Status != domain.PelletInProgress || p.Workspace == nil || p.Workspace.ID != run.WorkspaceID {
		return nil, domain.NewError(domain.Conflict, "implementation_ownership_changed", "the running pellet no longer has its captured ownership or implementation phase", nil)
	}
	// Every intervening generation must be a captured metadata edit on this run.
	// A release/reclaim, defer/reopen or scope replacement leaves a permanent gap.
	var count int64
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM execution_changes WHERE run_id=? AND from_revision>=? AND to_revision<=? AND delivered=0`, id, run.ImplementationRevision, p.ImplementationRevision).Scan(&count)
	if err != nil {
		return nil, err
	}
	if count != p.ImplementationRevision-run.ImplementationRevision {
		return nil, domain.NewError(domain.Conflict, "implementation_ownership_changed", "the pellet was released, reopened, or replaced; its old run cannot adopt that change", nil)
	}
	c := &storage.ExecutionChange{RunID: id}
	var oldJSON, newJSON, assessment string
	err = q.QueryRowContext(ctx, `SELECT from_revision,to_revision,old_json,new_json,assessment_json FROM execution_changes WHERE run_id=? AND from_revision=? AND delivered=0`, id, run.ImplementationRevision).Scan(&c.FromRevision, &c.ToRevision, &oldJSON, &newJSON, &assessment)
	if err != nil {
		return nil, err
	}
	c.Old, c.New = json.RawMessage(oldJSON), json.RawMessage(newJSON)
	if err = json.Unmarshal([]byte(assessment), &c.Assessment); err != nil {
		return nil, err
	}
	return c, nil
}

func (db *ProjectDatabase) PendingExecutionChange(ctx context.Context, id int64) (*storage.ExecutionChange, error) {
	// Read the run, queue generation and edit chain in one read transaction.
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, runStorageError(err)
	}
	defer tx.Rollback()
	change, err := pendingExecutionChange(ctx, tx, id)
	if err != nil {
		return nil, runStorageError(err)
	}
	return change, runStorageError(tx.Commit())
}

func sameExecutionChange(a, b *storage.ExecutionChange) bool {
	return a != nil && b != nil && a.RunID == b.RunID && a.FromRevision == b.FromRevision && a.ToRevision == b.ToRevision && string(a.Old) == string(b.Old) && string(a.New) == string(b.New)
}

func (db *ProjectDatabase) AssessExecutionChange(ctx context.Context, c storage.ExecutionChange, a storage.ChangeAssessment) error {
	if strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 4096 || len(a.FollowUp) > 8192 || a.Significant && strings.TrimSpace(a.FollowUp) == "" || a.ThreadID == "" || a.TurnID == "" {
		return storage.InvalidExecutionRun("invalid live edit assessment")
	}
	return db.writeRun(ctx, func(q *sql.Conn) error {
		pending, err := pendingExecutionChange(ctx, q, c.RunID)
		if err != nil {
			return err
		}
		if !sameExecutionChange(pending, &c) || pending.Assessment != nil {
			return storage.ExecutionRunConflict(c.RunID)
		}
		encoded, err := json.Marshal(a)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE execution_changes SET assessment_json=? WHERE run_id=? AND from_revision=?`, string(encoded), c.RunID, c.FromRevision)
		return err
	})
}

// Called only after a significant update was accepted by the implementation
// conversation (or after the assessor confirmed a cosmetic-only edit).
func (db *ProjectDatabase) AdoptExecutionChange(ctx context.Context, c storage.ExecutionChange) (out storage.ExecutionRun, err error) {
	err = db.writeRun(ctx, func(q *sql.Conn) error {
		pending, e := pendingExecutionChange(ctx, q, c.RunID)
		if e != nil {
			return e
		}
		if !sameExecutionChange(pending, &c) || pending.Assessment == nil {
			return storage.ExecutionRunConflict(c.RunID)
		}
		run, e := readExecutionRun(ctx, q, c.RunID)
		if e != nil {
			return e
		}
		if run.PendingOperation != "" {
			return storage.ExecutionRunConflict(c.RunID)
		}
		var target struct{ Title, Description string }
		if e = json.Unmarshal(c.New, &target); e != nil {
			return e
		}
		now := time.Now().UTC()
		if now.Before(run.UpdatedAt) {
			now = run.UpdatedAt
		}
		stamp := now.Format(runTimeFormat)
		summary := "Pellet edit assessed by Terra/max; updated requirements delivered."
		if !pending.Assessment.Significant {
			summary = "Pellet edit assessed by Terra/max; no implementation change needed."
		}
		_, e = q.ExecContext(ctx, `UPDATE execution_runs SET implementation_revision=?,pellet_title=?,pellet_description=?,revision=revision+1,updated_at=?,summary=? WHERE run_id=?`, c.ToRevision, target.Title, target.Description, stamp, summary, c.RunID)
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `UPDATE execution_changes SET delivered=1 WHERE run_id=? AND from_revision=?`, c.RunID, c.FromRevision)
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO execution_run_activity(run_id,revision,at,phase,state,summary,operation) VALUES(?,?,?,?,?,?,'')`, run.ID, run.Revision+1, stamp, run.Phase, run.State, summary)
		if e != nil {
			return e
		}
		out, e = readExecutionRun(ctx, q, c.RunID)
		return e
	})
	return out, err
}

var _ storage.ExecutionChangeDatabase = (*ProjectDatabase)(nil)
