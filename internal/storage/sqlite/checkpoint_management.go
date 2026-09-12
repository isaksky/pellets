package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// checkpointMutation centralizes project isolation, optimistic concurrency,
// and the writer lock for operations that change scope or queue visibility.
func (writer *WebWriter) checkpointMutation(ctx context.Context, project storage.Project, reference domain.PelletReference, version string, mutate func(*sql.Conn, storage.ResolvedProject, storage.Pellet, float64) error) (storage.Pellet, error) {
	if err := validateWebVersion(version); err != nil {
		return storage.Pellet{}, err
	}
	resolved, err := resolvedForScalarWrite(project)
	if err != nil {
		return storage.Pellet{}, err
	}
	connection, err := writer.db.Conn(ctx)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("open checkpoint connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Pellet{}, pelletStorageError("begin checkpoint mutation", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := ensureStoredProject(ctx, connection, project); err != nil {
		return storage.Pellet{}, err
	}
	if err := ensureReferenceProject(ctx, connection, project, reference); err != nil {
		return storage.Pellet{}, err
	}
	before, err := loadPellet(ctx, connection, project.ID, reference.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Pellet{}, pelletNotFound(reference)
	}
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read checkpoint", err)
	}
	if version != storage.PelletVersion(before) {
		return storage.Pellet{}, &storage.OptimisticConflict{Pellet: &before}
	}
	if before.Kind != domain.PelletReviewCheckpoint {
		return storage.Pellet{}, invalidCheckpoint("this operation requires a review checkpoint")
	}
	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("capture checkpoint mutation timestamp", err)
	}
	if err := mutate(connection, resolved, before, timestamp); err != nil {
		return storage.Pellet{}, err
	}
	after, err := loadPellet(ctx, connection, project.ID, reference.Number)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read changed checkpoint", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Pellet{}, pelletStorageError("commit checkpoint mutation", err)
	}
	committed = true
	return after, nil
}

func requireOpenCheckpoint(p storage.Pellet) error {
	if p.Status != domain.PelletOpen || p.Workspace != nil {
		return domain.NewError(domain.Conflict, "checkpoint_not_editable", "only an open, unowned checkpoint may change scope or be removed; release owned work first", map[string]any{"pellet": p.Reference.String(), "status": p.Status})
	}
	return nil
}

func preserveCheckpointScope(ctx context.Context, q projectQuery, p storage.Pellet, timestamp float64) error {
	encoded, err := json.Marshal(p.Checkpoint)
	if err != nil {
		return pelletStorageError("encode historical checkpoint scope", err)
	}
	_, err = q.ExecContext(ctx, `INSERT INTO review_checkpoint_scope_history(project_id,checkpoint_number,implementation_revision,scope_json,captured_at) VALUES(?,?,?,?,?)`, p.ProjectID, p.Reference.Number, p.ImplementationRevision, string(encoded), timestamp)
	if err != nil {
		return pelletStorageError("preserve historical checkpoint scope", err)
	}
	return nil
}

// UpdateWebCheckpointScope deliberately snapshots the selected current target
// rows into a new checkpoint generation. The old materialized scope is saved
// before replacement; execution captures, findings and triage are untouched.
func (writer *WebWriter) UpdateWebCheckpointScope(ctx context.Context, project storage.Project, reference domain.PelletReference, version string, selected []storage.ReviewTargetVersion) (storage.Pellet, error) {
	input := storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Status: domain.PelletOpen, ReviewTargetVersions: selected}
	for _, target := range selected {
		input.ReviewTargets = append(input.ReviewTargets, target.Reference)
	}
	if err := validateCheckpointInput(&input); err != nil {
		return storage.Pellet{}, err
	}
	return writer.checkpointMutation(ctx, project, reference, version, func(q *sql.Conn, resolved storage.ResolvedProject, before storage.Pellet, timestamp float64) error {
		if err := requireOpenCheckpoint(before); err != nil {
			return err
		}
		targets, err := prepareCheckpoint(ctx, q, resolved, &input)
		if err != nil {
			return err
		}
		if err := preserveCheckpointScope(ctx, q, before, timestamp); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM review_checkpoint_targets WHERE project_id=? AND checkpoint_number=?`, project.ID, reference.Number); err != nil {
			return pelletStorageError("replace current checkpoint scope", err)
		}
		for ordinal, target := range targets {
			if _, err := q.ExecContext(ctx, `INSERT INTO review_checkpoint_targets(project_id,checkpoint_number,target_number,ordinal,selected_reference,title,description,external_id,group_id) VALUES(?,?,?,?,?,?,?,?,?)`, project.ID, reference.Number, target.Reference.Number, ordinal, target.Reference.String(), target.Title, target.Description, target.ExternalID, target.Group); err != nil {
				return pelletStorageError("insert changed checkpoint target", err)
			}
		}
		if _, err := q.ExecContext(ctx, `UPDATE pellets SET implementation_revision=implementation_revision+1,updated_at=? WHERE project_id=? AND number=?`, timestamp, project.ID, reference.Number); err != nil {
			return pelletStorageError("advance checkpoint scope generation", err)
		}
		return nil
	})
}

func (writer *WebWriter) RemoveWebCheckpoint(ctx context.Context, project storage.Project, reference domain.PelletReference, version string) (storage.Pellet, error) {
	return writer.checkpointMutation(ctx, project, reference, version, func(q *sql.Conn, resolved storage.ResolvedProject, before storage.Pellet, timestamp float64) error {
		if err := requireOpenCheckpoint(before); err != nil {
			return err
		}
		if err := preserveCheckpointScope(ctx, q, before, timestamp); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO review_checkpoint_removals(project_id,checkpoint_number,previous_number,next_number,original_priority,removed_at)
 SELECT project_id,number,
 (SELECT p.number FROM pellets p WHERE p.project_id=cp.project_id AND p.status IN ('open','in_progress') AND p.priority<cp.priority ORDER BY p.priority DESC,p.number DESC LIMIT 1),
 (SELECT p.number FROM pellets p WHERE p.project_id=cp.project_id AND p.status IN ('open','in_progress') AND p.priority>cp.priority ORDER BY p.priority,p.number LIMIT 1),
 priority,? FROM pellets cp WHERE cp.project_id=? AND cp.number=?`, timestamp, project.ID, reference.Number); err != nil {
			return pelletStorageError("record checkpoint removal position", err)
		}
		if _, err := q.ExecContext(ctx, `UPDATE pellets SET status='maybe_later',priority=NULL,updated_at=? WHERE project_id=? AND number=?`, timestamp, project.ID, reference.Number); err != nil {
			return pelletStorageError("defer removed checkpoint", err)
		}
		return nil
	})
}

func (writer *WebWriter) RestoreWebCheckpoint(ctx context.Context, project storage.Project, reference domain.PelletReference, version string) (storage.Pellet, error) {
	return writer.checkpointMutation(ctx, project, reference, version, func(q *sql.Conn, resolved storage.ResolvedProject, before storage.Pellet, timestamp float64) error {
		if before.Status != domain.PelletMaybeLater || before.Checkpoint == nil || !before.Checkpoint.Removed {
			return domain.NewError(domain.Conflict, "checkpoint_not_removed", "only a removed checkpoint can be restored to its saved queue position", map[string]any{"pellet": before.Reference.String()})
		}
		var previous, next sql.NullInt64
		var original int64
		if err := q.QueryRowContext(ctx, `SELECT previous_number,next_number,original_priority FROM review_checkpoint_removals WHERE project_id=? AND checkpoint_number=?`, project.ID, reference.Number).Scan(&previous, &next, &original); err != nil {
			return pelletStorageError("read checkpoint restore position", err)
		}
		var placement *storage.PelletPlacement
		for _, anchor := range []struct {
			number sql.NullInt64
			before bool
		}{{next, true}, {previous, false}} {
			if !anchor.number.Valid {
				continue
			}
			p, err := loadPellet(ctx, q, project.ID, anchor.number.Int64)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return pelletStorageError("read restore anchor", err)
			}
			if p.Priority != nil && (p.Status == domain.PelletOpen || p.Status == domain.PelletInProgress) {
				placement = &storage.PelletPlacement{Target: p.Reference, Before: anchor.before}
				break
			}
		}
		// If both original neighbors disappeared, use the nearest remaining
		// priority at or after the old position, otherwise the current tail.
		if placement == nil {
			var number int64
			err := q.QueryRowContext(ctx, `SELECT number FROM pellets WHERE project_id=? AND status IN ('open','in_progress') AND priority>=? ORDER BY priority,number LIMIT 1`, project.ID, original).Scan(&number)
			if err == nil {
				placement = &storage.PelletPlacement{Target: domain.PelletReference{ProjectCode: project.Code, Number: number}, Before: true}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return pelletStorageError("find checkpoint restore fallback", err)
			}
		}
		var priority int64
		var err error
		if placement == nil {
			priority, err = allocateTailPriority(ctx, q, project.ID)
		} else {
			priority, err = allocatePlacedPriority(ctx, q, resolved, *placement, reference.Number)
		}
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE pellets SET status='open',priority=?,updated_at=? WHERE project_id=? AND number=?`, priority, timestamp, project.ID, reference.Number); err != nil {
			return pelletStorageError("restore checkpoint", err)
		}
		return nil
	})
}

func (reader *WebReader) ReadWebCheckpointHistory(ctx context.Context, project storage.Project, reference domain.PelletReference) ([]storage.CheckpointScopeHistory, error) {
	if err := ensureStoredProject(ctx, reader.db, project); err != nil {
		return nil, err
	}
	if err := ensureReferenceProject(ctx, reader.db, project, reference); err != nil {
		return nil, err
	}
	rows, err := reader.db.QueryContext(ctx, `
 SELECT implementation_revision,scope_json,strftime('%Y-%m-%dT%H:%M:%fZ',captured_at)
 FROM (
   SELECT implementation_revision,scope_json,captured_at FROM review_checkpoint_scope_history WHERE project_id=? AND checkpoint_number=?
   UNION ALL
   SELECT r.implementation_revision,r.checkpoint_scope_json,r.created_at FROM execution_runs r
   JOIN pellets p ON p.project_id=r.project_id AND p.number=r.pellet_number
   WHERE r.project_id=? AND r.pellet_number=? AND r.mode='review_checkpoint'
     AND r.implementation_revision<p.implementation_revision AND r.checkpoint_scope_json<>'null'
     AND r.run_id=(SELECT max(latest.run_id) FROM execution_runs latest WHERE latest.project_id=r.project_id AND latest.pellet_number=r.pellet_number AND latest.implementation_revision=r.implementation_revision AND latest.mode='review_checkpoint')
     AND NOT EXISTS (SELECT 1 FROM review_checkpoint_scope_history h WHERE h.project_id=r.project_id AND h.checkpoint_number=r.pellet_number AND h.implementation_revision=r.implementation_revision)
 ) ORDER BY implementation_revision DESC`, project.ID, reference.Number, project.ID, reference.Number)
	if err != nil {
		return nil, pelletStorageError("read checkpoint scope history", err)
	}
	defer rows.Close()
	var history []storage.CheckpointScopeHistory
	for rows.Next() {
		var record storage.CheckpointScopeHistory
		var encoded, timestamp string
		if err := rows.Scan(&record.ImplementationRevision, &encoded, &timestamp); err != nil {
			return nil, pelletStorageError("read historical checkpoint scope", err)
		}
		if err := json.Unmarshal([]byte(encoded), &record.Scope); err != nil {
			return nil, pelletStorageError("decode historical checkpoint scope", err)
		}
		record.CapturedAt, err = parseProjectTimestamp("scope captured_at", timestamp)
		if err != nil {
			return nil, err
		}
		for i := range record.Scope.Targets {
			record.Scope.Targets[i].Reference = (domain.PelletReference{ProjectCode: project.Code, Number: record.Scope.Targets[i].Number}).String()
		}
		history = append(history, record)
	}
	return history, rows.Err()
}
