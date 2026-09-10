package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// Both selection and the materialized JSON use the same relational readiness
// rule in the same SQLite snapshot. There is no cached readiness flag.
const checkpointEligibleSQL = `(p.kind = 'ordinary' OR (
 EXISTS (SELECT 1 FROM review_checkpoint_targets t WHERE t.project_id=p.project_id AND t.checkpoint_number=p.number)
 AND NOT EXISTS (SELECT 1 FROM review_checkpoint_readiness t WHERE t.project_id=p.project_id AND t.checkpoint_number=p.number AND t.reason<>'ready')
))`

const checkpointJSONSQL = `(SELECT json_group_array(json_object(
 'project_id', t.project_id, 'number', t.target_number,
 'reference', project.code || '-' || t.target_number,
 'selected_reference', t.selected_reference,
 'title', t.title, 'description', t.description, 'external_id', t.external_id, 'group', t.group_id,
 'status', t.target_status, 'implementation_revision', t.implementation_revision, 'reason', t.reason,
 'evidence', CASE WHEN t.run_id IS NULL THEN NULL ELSE json_object(
   'run_id',t.run_id,'workspace_id',t.workspace_id,'starting_head',t.starting_head,'result_commit',t.result_commit) END
 )) FROM (SELECT * FROM review_checkpoint_readiness
 WHERE project_id=p.project_id AND checkpoint_number=p.number ORDER BY ordinal) t)`

func invalidCheckpoint(message string) error {
	return domain.NewError(domain.Usage, "invalid_review_checkpoint", message, nil)
}

func requireCheckpointReady(p storage.Pellet) error {
	if p.Kind == domain.PelletReviewCheckpoint && (p.Checkpoint == nil || !p.Checkpoint.Ready) {
		return domain.NewError(domain.Conflict, "review_checkpoint_not_ready", "every selected target must retain the selected scope, be closed, and have matching verified implementation evidence", map[string]any{"pellet": p.Reference.String(), "checkpoint": p.Checkpoint})
	}
	return nil
}

func validateCheckpointClose(ctx context.Context, q projectQuery, p storage.Pellet) error {
	if p.Kind != domain.PelletReviewCheckpoint {
		return nil
	}
	var id, revision int64
	var encoded string
	err := q.QueryRowContext(ctx, `SELECT run_id,implementation_revision,checkpoint_scope_json FROM execution_runs WHERE project_id=? AND pellet_number=? AND mode='review_checkpoint' ORDER BY run_id DESC LIMIT 1`, p.ProjectID, p.Reference.Number).Scan(&id, &revision, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var scope *storage.ReviewCheckpoint
	if err := json.Unmarshal([]byte(encoded), &scope); err != nil {
		return err
	}
	if revision != p.ImplementationRevision || !storage.SameReviewScope(scope, p.Checkpoint) {
		return storage.ExecutionRunConflict(id)
	}
	return nil
}

// Selection validation, snapshot capture and placement run under the creation
// writer transaction. Ordering is canonical by number, not caller order.
func prepareCheckpoint(ctx context.Context, q projectQuery, project storage.ResolvedProject, input *storage.NewPellet) ([]storage.Pellet, error) {
	if input.Kind != domain.PelletReviewCheckpoint {
		return nil, nil
	}
	targets := make([]storage.Pellet, 0, len(input.ReviewTargets))
	var last *storage.Pellet
	for _, ref := range input.ReviewTargets {
		if err := ensureReferenceProject(ctx, q, project.Project, ref); err != nil {
			return nil, err
		}
		target, err := loadPellet(ctx, q, project.Project.ID, ref.Number)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, pelletNotFound(ref)
		}
		if err != nil {
			return nil, pelletStorageError("read review target", err)
		}
		if target.Kind != domain.PelletOrdinary {
			return nil, invalidCheckpoint("review checkpoints may select only ordinary pellets")
		}
		targets = append(targets, target)
		if target.Priority != nil && (last == nil || *target.Priority > *last.Priority || (*target.Priority == *last.Priority && target.Reference.Number > last.Reference.Number)) {
			copy := target
			last = &copy
		}
	}
	if last != nil {
		input.Placement = &storage.PelletPlacement{Target: last.Reference}
	}
	return targets, nil
}

func validateCheckpointInput(input *storage.NewPellet) error {
	if input.Kind == domain.PelletOrdinary {
		input.Kind = ""
	}
	if input.Kind == "" {
		if input.ReviewTargets != nil {
			return invalidCheckpoint("review targets require kind review_checkpoint")
		}
		return nil
	}
	if input.Kind != domain.PelletReviewCheckpoint {
		return invalidCheckpoint("kind must be ordinary or review_checkpoint")
	}
	if len(input.ReviewTargets) == 0 || len(input.ReviewTargets) > 1000 {
		return invalidCheckpoint("select between 1 and 1000 explicit review targets")
	}
	if input.Placement != nil || input.Status != domain.PelletOpen {
		return invalidCheckpoint("review checkpoints are created open with automatic placement")
	}
	input.ReviewTargets = append([]domain.PelletReference(nil), input.ReviewTargets...)
	sort.Slice(input.ReviewTargets, func(i, j int) bool { return input.ReviewTargets[i].Number < input.ReviewTargets[j].Number })
	for i, ref := range input.ReviewTargets {
		if _, err := domain.ParsePelletReference(ref.String()); err != nil {
			return err
		}
		if i > 0 && input.ReviewTargets[i-1].Number == ref.Number && input.ReviewTargets[i-1].ProjectCode == ref.ProjectCode {
			return invalidCheckpoint("review targets must be distinct")
		}
	}
	return nil
}
