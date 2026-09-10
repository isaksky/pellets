package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

const runTimeFormat = "2006-01-02T15:04:05.000000000Z"

func OpenExecutionRunDatabase(ctx context.Context, path string) (*ProjectDatabase, error) {
	return OpenProjectDatabase(ctx, path)
}

// Creation captures the pellet under the same lock as allocation. Resuming
// links the exact stopped attempt, never the newest pellet or a project code.
func (db *ProjectDatabase) CreateExecutionRun(ctx context.Context, c storage.RunCapture) (run storage.ExecutionRun, err error) {
	if err = storage.ValidateRunCapture(c); err != nil {
		return run, err
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		var title, description, status string
		var workspace sql.NullInt64
		var externalID, group sql.NullString
		var pellet storage.Pellet
		err := conn.QueryRowContext(ctx, `SELECT title, description, status, workspace_id, external_id, group_id, kind, implementation_revision FROM pellets WHERE project_id = ? AND number = ?`, c.ProjectID, c.PelletNumber).Scan(&title, &description, &status, &workspace, &externalID, &group, &pellet.Kind, &pellet.ImplementationRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return storage.InvalidExecutionRun("the exact pellet no longer exists")
		}
		if err != nil {
			return err
		}
		implementationRevision := pellet.ImplementationRevision
		if pellet.Kind == domain.PelletReviewCheckpoint {
			pellet, err = loadPellet(ctx, conn, c.ProjectID, c.PelletNumber)
			if err != nil {
				return err
			}
			if c.Mode != "review_checkpoint" {
				return storage.InvalidExecutionRun("review checkpoints require the separate review driver")
			}
			if err := requireCheckpointReady(pellet); err != nil {
				return err
			}
			if !(c.ResumeFrom != nil && status == "closed") && (status != "in_progress" || workspace.Int64 != c.WorkspaceID) {
				return storage.InvalidExecutionRun("review must use this workspace's in-progress checkpoint")
			}
			if !storage.MatchesSchedule(pellet, c.ExternalID, c.Group) {
				return storage.InvalidExecutionRun("checkpoint no longer matches captured filters")
			}
		}
		resumingClosed := c.ResumeFrom != nil && status == "closed"
		if resumingClosed {
			var occupied bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pellets WHERE project_id=? AND workspace_id=? AND status='in_progress')`, c.ProjectID, c.WorkspaceID).Scan(&occupied); err != nil {
				return err
			}
			if occupied {
				return storage.InvalidExecutionRun("closed-pellet reconciliation requires a workspace without another in-progress pellet")
			}
		}
		if c.Mode != "review_checkpoint" && !resumingClosed && (status != "in_progress" || workspace.Int64 != c.WorkspaceID) {
			return storage.InvalidExecutionRun("implementation must use this workspace's in-progress pellet")
		}
		if c.Mode != "review_checkpoint" && (c.ExternalID != nil && (!externalID.Valid || *c.ExternalID != externalID.String) || c.Group != nil && (!group.Valid || *c.Group != group.String)) {
			return storage.InvalidExecutionRun("the exact pellet no longer matches the captured filters")
		}
		if len(title) > storage.MaxRunSnapshotBytes || len(description) > storage.MaxRunSnapshotBytes {
			return storage.InvalidExecutionRun("pellet snapshot exceeds the storage bound; it cannot be silently truncated")
		}
		var active int64
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(run_id), 0) FROM execution_runs WHERE workspace_id = ? AND (state IN ('running','awaiting_input') OR pending_operation <> '')`, c.WorkspaceID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return storage.ExecutionRunConflict(active)
		}
		threadID := ""
		turnID, finalization, phase := "", "null", "preflight"
		resultCommit := ""
		var verifiedStamp *string
		if c.ResumeFrom != nil {
			previous, err := readExecutionRun(ctx, conn, *c.ResumeFrom)
			if err != nil {
				return err
			}
			completedReceipt := previous.State == "completed" && resumingClosed && previous.Finalization != nil && previous.ResultCommit != "" && previous.Phase == "finalization"
			if (pellet.Kind == domain.PelletReviewCheckpoint && previous.ImplementationRevision != pellet.ImplementationRevision) || !storage.SameReviewScope(previous.CheckpointScope, pellet.Checkpoint) {
				return storage.ExecutionRunConflict(previous.ID)
			}
			// A continuation retains the generation it actually implemented,
			// including unknown legacy generations. Resume cannot manufacture
			// fresh review evidence after a reopen or scope edit.
			implementationRevision = previous.ImplementationRevision
			if previous.ProjectID != c.ProjectID || previous.WorkspaceID != c.WorkspaceID || previous.PelletNumber != c.PelletNumber || storage.RunActive(previous.State) || previous.State == "completed" && !completedReceipt {
				return storage.ExecutionRunConflict(previous.ID)
			}
			if !reflect.DeepEqual(previous.ExternalID, c.ExternalID) || !reflect.DeepEqual(previous.Group, c.Group) {
				return storage.ExecutionRunConflict(previous.ID)
			}
			if resumingClosed && (previous.Finalization == nil || previous.ResultCommit == "" || previous.Phase != "close" && !completedReceipt) {
				return storage.ExecutionRunConflict(previous.ID)
			}
			c.StartingHead = previous.StartingHead
			c.StartingRef = previous.StartingRef
			c.Mode = previous.Mode
			c.ScheduleMode, c.ScheduleRemaining = previous.ScheduleMode, previous.ScheduleRemaining
			c.ExternalID, c.Group = previous.ExternalID, previous.Group
			if previous.PelletTitle != title || previous.PelletDescription != description {
				return storage.ExecutionRunConflict(previous.ID)
			}
			var alreadyResumed bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM execution_runs WHERE resume_from = ?)`, previous.ID).Scan(&alreadyResumed); err != nil {
				return err
			}
			if alreadyResumed {
				return storage.ExecutionRunConflict(previous.ID)
			}
			threadID = previous.ThreadID
			phase, turnID = previous.Phase, previous.TurnID
			if completedReceipt {
				phase = "close"
			}
			if previous.Finalization != nil {
				resultCommit = previous.ResultCommit
				if previous.CommitVerifiedAt != nil {
					stamp := previous.CommitVerifiedAt.Format(runTimeFormat)
					verifiedStamp = &stamp
				}
				encoded, err := json.Marshal(previous.Finalization)
				if err != nil {
					return err
				}
				finalization = string(encoded)
			}
			if previous.ThreadID != "" {
				// A resumed conversation keeps the exact prefix that established
				// its context. PrepareRun may have freshly observed changed
				// skill/tool bytes, but the scheduler deliberately does not
				// append them to this existing thread.
				c.PromptPrefix = previous.PromptPrefix
			}
		}
		if c.ScheduleMode == "" {
			c.ScheduleMode = c.Mode
			if c.Mode == "review_checkpoint" {
				c.ScheduleMode = "run_one"
			}
		}
		if c.ScheduleRemaining == 0 {
			c.ScheduleRemaining = 1
		}
		encoded, err := json.Marshal(c.Settings)
		if err != nil {
			return err
		}
		prefix, err := json.Marshal(c.PromptPrefix)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(runTimeFormat)
		result, err := conn.ExecContext(ctx, `INSERT INTO execution_runs(
			project_id, workspace_id, pellet_number, attempt, resume_from, mode, external_id, group_id,
		settings_json, prompt_prefix_json, starting_head, pellet_title, pellet_description, thread_id, turn_id, finalization_json, result_commit, commit_verified_at, phase, state, revision, created_at, updated_at)
			VALUES (?, ?, ?, (SELECT COALESCE(MAX(attempt), 0) + 1 FROM execution_runs WHERE project_id = ? AND pellet_number = ?),
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', 1, ?, ?)`,
			c.ProjectID, c.WorkspaceID, c.PelletNumber, c.ProjectID, c.PelletNumber, c.ResumeFrom,
			c.Mode, c.ExternalID, c.Group, string(encoded), string(prefix), c.StartingHead, title, description, threadID, turnID, finalization, resultCommit, verifiedStamp, phase, now, now)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		scopeJSON, err := json.Marshal(pellet.Checkpoint)
		if err != nil {
			return err
		}
		if len(scopeJSON) > storage.MaxRunSnapshotBytes {
			return storage.InvalidExecutionRun("review scope exceeds the snapshot storage bound; it cannot be silently truncated")
		}
		if _, err := conn.ExecContext(ctx, `UPDATE execution_runs SET implementation_revision=?, checkpoint_scope_json=? WHERE run_id=?`, implementationRevision, string(scopeJSON), id); err != nil {
			return err
		}
		if err := appendRunActivity(ctx, conn, id, 1, now, storage.RunProgress{Phase: phase, State: "running"}); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE execution_runs SET starting_ref=?, schedule_mode=?, schedule_remaining=? WHERE run_id=?`, c.StartingRef, c.ScheduleMode, c.ScheduleRemaining, id); err != nil {
			return err
		}
		run, err = readExecutionRun(ctx, conn, id)
		return err
	})
	return run, err
}

func (db *ProjectDatabase) ReadExecutionRun(ctx context.Context, id int64) (run storage.ExecutionRun, err error) {
	if id < 1 {
		return run, storage.InvalidExecutionRun("run ID must be positive")
	}
	// Read row and activity in a single snapshot without holding a writer lock.
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return run, runStorageError(err)
	}
	defer tx.Rollback()
	run, err = readExecutionRun(ctx, tx, id)
	if err == nil {
		err = tx.Commit()
	}
	return run, runStorageError(err)
}

// beforeID is an exclusive, stable pagination cursor; zero starts at newest.
func (db *ProjectDatabase) ListWorkspaceRuns(ctx context.Context, workspaceID, beforeID int64, limit int) (runs []storage.ExecutionRun, err error) {
	if workspaceID < 1 || beforeID < 0 || limit < 1 || limit > 100 {
		return nil, storage.InvalidExecutionRun("run listing requires a workspace, nonnegative cursor, and limit 1..100")
	}
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, runStorageError(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT run_id FROM execution_runs WHERE workspace_id = ? AND (? = 0 OR run_id < ?) ORDER BY run_id DESC LIMIT ?`, workspaceID, beforeID, beforeID, limit)
	if err != nil {
		return nil, runStorageError(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return nil, runStorageError(err)
	}
	runs = make([]storage.ExecutionRun, 0, len(ids))
	for _, id := range ids {
		run, readErr := readExecutionRun(ctx, tx, id)
		if readErr != nil {
			return nil, runStorageError(readErr)
		}
		runs = append(runs, run)
	}
	return runs, runStorageError(tx.Commit())
}

func (db *ProjectDatabase) UpdateExecutionRun(ctx context.Context, request storage.UpdateExecutionRun) (run storage.ExecutionRun, err error) {
	if request.ID < 1 || request.ExpectedRevision < 1 {
		return run, storage.InvalidExecutionRun("run ID and revision must be positive")
	}
	if err := storage.ValidateRunProgress(request.Progress); err != nil {
		return run, err
	}
	if request.VerifiedCommit != "" && !storage.IsFullCommitID(request.VerifiedCommit) {
		return run, storage.InvalidExecutionRun("verified commit must be a full object ID")
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		current, err := readExecutionRun(ctx, conn, request.ID)
		if err != nil {
			return err
		}
		if current.Revision != request.ExpectedRevision || !storage.RunActive(current.State) {
			return storage.ExecutionRunConflict(current.ID)
		}
		p := request.Progress
		if p.State == "completed" && current.CheckpointScope != nil {
			pellet, err := loadPellet(ctx, conn, current.ProjectID, current.PelletNumber)
			if err != nil {
				return err
			}
			if err := requireCheckpointReady(pellet); err != nil {
				return err
			}
			if pellet.ImplementationRevision != current.ImplementationRevision || !storage.SameReviewScope(pellet.Checkpoint, current.CheckpointScope) {
				return storage.ExecutionRunConflict(current.ID)
			}
		}
		if current.Finalization != nil && !reflect.DeepEqual(current.Finalization, p.Finalization) {
			return storage.ExecutionRunConflict(current.ID)
		}
		finalization, err := json.Marshal(p.Finalization)
		if err != nil {
			return err
		}
		interaction, err := json.Marshal(p.Interaction)
		if err != nil {
			return err
		}
		// Conversation identity cannot silently change. A new thread requires a
		// separate explicit attempt; a new turn is legal only at turn_start.
		if (current.ThreadID != "" && p.ThreadID != current.ThreadID) || (current.TurnID != "" && p.TurnID != current.TurnID && (p.Phase != "turn_start" || p.TurnID == "")) {
			return storage.ExecutionRunConflict(current.ID)
		}
		if request.VerifiedCommit != "" && current.ResultCommit != "" && current.ResultCommit != request.VerifiedCommit {
			return storage.ExecutionRunConflict(current.ID)
		}
		now := time.Now().UTC()
		if now.Before(current.UpdatedAt) {
			now = current.UpdatedAt
		}
		stamp := now.Format(runTimeFormat)
		resultCommit, verified := current.ResultCommit, current.CommitVerifiedAt
		if request.VerifiedCommit != "" && resultCommit == "" {
			resultCommit, verified = request.VerifiedCommit, &now
		}
		if p.State == "completed" && (resultCommit == "" || p.ThreadID == "" || p.TurnID == "") {
			return storage.InvalidExecutionRun("completion requires verified commit and Codex thread/turn evidence")
		}
		var finished, verifiedStamp *string
		if !storage.RunActive(p.State) {
			finished = &stamp
		}
		if verified != nil {
			s := verified.Format(runTimeFormat)
			verifiedStamp = &s
		}
		_, err = conn.ExecContext(ctx, `UPDATE execution_runs SET phase=?, state=?, thread_id=?, turn_id=?, outcome=?, error_code=?, summary=?, cached_input_tokens=?, finalization_json=?, interaction_json=?, result_commit=?, commit_verified_at=?, revision=revision+1, updated_at=?, finished_at=? WHERE run_id=?`,
			p.Phase, p.State, p.ThreadID, p.TurnID, p.Outcome, p.ErrorCode, p.Summary, p.CachedInputTokens, string(finalization), string(interaction), resultCommit, verifiedStamp, stamp, finished, current.ID)
		if err != nil {
			return err
		}
		if err := appendRunActivity(ctx, conn, current.ID, current.Revision+1, stamp, p); err != nil {
			return err
		}
		run, err = readExecutionRun(ctx, conn, current.ID)
		return err
	})
	return run, err
}

// InterruptExecutionRun is a supervisor-only transition after owned processes
// have stopped. A cancelled RPC may already have marked needs_attention; retain
// its identities and pending marker while recording the foreground interruption.
// This never clears uncertain operations or permits replay by itself.
func (db *ProjectDatabase) InterruptExecutionRun(ctx context.Context, id, revision int64, outcome string) (run storage.ExecutionRun, err error) {
	if id < 1 || revision < 1 {
		return run, storage.InvalidExecutionRun("run ID and revision must be positive")
	}
	if outcome != "unknown" && outcome != "cancelled" && outcome != "failed" {
		return run, storage.InvalidExecutionRun("interruption requires an unknown or observed nonsuccess outcome")
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		current, err := readExecutionRun(ctx, conn, id)
		if err != nil {
			return err
		}
		if current.Revision != revision || current.State == "completed" {
			return storage.ExecutionRunConflict(id)
		}
		if current.State == "interrupted" {
			run = current
			return nil
		}
		progress := current.RunProgress
		progress.State, progress.Outcome, progress.ErrorCode = "interrupted", outcome, "supervisor_stopped"
		stamp := runUpdateTime(current).Format(runTimeFormat)
		progress.Interaction = nil
		_, err = conn.ExecContext(ctx, `UPDATE execution_runs SET state='interrupted', outcome=?, error_code='supervisor_stopped', interaction_json='null', revision=revision+1, updated_at=?, finished_at=? WHERE run_id=?`, outcome, stamp, stamp, id)
		if err != nil {
			return err
		}
		if err := appendRunActivity(ctx, conn, id, current.Revision+1, stamp, progress); err != nil {
			return err
		}
		run, err = readExecutionRun(ctx, conn, id)
		return err
	})
	return run, err
}

// BeginExecutionOperation serializes external calls without holding a SQLite
// transaction across the call. Progress may advance while this marker remains.
func (db *ProjectDatabase) BeginExecutionOperation(ctx context.Context, id, revision int64, operation string) (run storage.ExecutionRun, err error) {
	if id < 1 || revision < 1 {
		return run, storage.InvalidExecutionRun("run ID and revision must be positive")
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		current, err := readExecutionRun(ctx, conn, id)
		if err != nil {
			return err
		}
		if current.Revision != revision || !storage.RunActive(current.State) || current.PendingOperation != "" {
			return storage.ExecutionRunConflict(id)
		}
		progress := current.RunProgress
		switch operation {
		case "thread/start":
			if current.ThreadID != "" {
				return storage.ExecutionRunConflict(id)
			}
			progress.Phase = "thread_start"
		case "thread/resume":
			if current.ThreadID == "" {
				return storage.ExecutionRunConflict(id)
			}
			progress.Phase = "thread_start"
		case "turn/start":
			if current.ThreadID == "" {
				return storage.ExecutionRunConflict(id)
			}
			progress.Phase = "turn_start"
		case "turn/interrupt":
			if current.TurnID == "" {
				return storage.ExecutionRunConflict(id)
			}
		default:
			return storage.InvalidExecutionRun("unsupported pending execution operation")
		}
		stamp := runUpdateTime(current).Format(runTimeFormat)
		_, err = conn.ExecContext(ctx, `UPDATE execution_runs SET phase=?, pending_operation=?, pending_revision=revision+1, pending_turn_id=turn_id, revision=revision+1, updated_at=? WHERE run_id=?`, progress.Phase, operation, stamp, id)
		if err != nil {
			return err
		}
		if err := appendRunActivity(ctx, conn, id, revision+1, stamp, progress, operation); err != nil {
			return err
		}
		run, err = readExecutionRun(ctx, conn, id)
		return err
	})
	return run, err
}

// FinishExecutionOperation reconciles only the response's identities against
// the exact pending call. It preserves intervening state, phase, summaries,
// commit evidence and terminal outcomes rather than requiring the old row
// revision or replaying an obsolete progress snapshot.
func (db *ProjectDatabase) FinishExecutionOperation(ctx context.Context, result storage.ExecutionOperationResult) (run storage.ExecutionRun, err error) {
	if result.ID < 1 || result.PendingRevision < 1 {
		return run, storage.InvalidExecutionRun("run ID and pending revision must be positive")
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		current, err := readExecutionRun(ctx, conn, result.ID)
		if err != nil {
			return err
		}
		if current.PendingOperation == "" || current.PendingRevision != result.PendingRevision {
			return storage.ExecutionRunConflict(result.ID)
		}
		progress := current.RunProgress
		finished := current.FinishedAt
		now := runUpdateTime(current)
		if result.ErrorCode == "" {
			switch current.PendingOperation {
			case "thread/start", "thread/resume":
				if result.ThreadID == "" || (current.ThreadID != "" && current.ThreadID != result.ThreadID) || result.TurnID != "" {
					return storage.ExecutionRunConflict(result.ID)
				}
				progress.ThreadID = result.ThreadID
			case "turn/start":
				if result.TurnID == "" || result.ThreadID != current.ThreadID || (current.TurnID != current.PendingTurnID && current.TurnID != result.TurnID) {
					return storage.ExecutionRunConflict(result.ID)
				}
				progress.TurnID = result.TurnID
			case "turn/interrupt":
				if result.ThreadID != "" || result.TurnID != "" {
					return storage.ExecutionRunConflict(result.ID)
				}
			}
		} else if storage.RunActive(current.State) {
			progress.State, progress.Outcome, progress.ErrorCode = "needs_attention", "unknown", result.ErrorCode
			progress.Interaction = nil
			finished = &now
		}
		if err := storage.ValidateRunProgress(progress); err != nil {
			return err
		}
		interaction, err := json.Marshal(progress.Interaction)
		if err != nil {
			return err
		}
		var finishedStamp *string
		if finished != nil {
			s := finished.Format(runTimeFormat)
			finishedStamp = &s
		}
		stamp := now.Format(runTimeFormat)
		_, err = conn.ExecContext(ctx, `UPDATE execution_runs SET thread_id=?, turn_id=?, state=?, outcome=?, error_code=?, interaction_json=?, finished_at=?, pending_operation='', pending_revision=0, pending_turn_id='', revision=revision+1, updated_at=? WHERE run_id=?`,
			progress.ThreadID, progress.TurnID, progress.State, progress.Outcome, progress.ErrorCode, string(interaction), finishedStamp, stamp, result.ID)
		if err != nil {
			return err
		}
		if err := appendRunActivity(ctx, conn, result.ID, current.Revision+1, stamp, progress, current.PendingOperation); err != nil {
			return err
		}
		run, err = readExecutionRun(ctx, conn, result.ID)
		return err
	})
	return run, err
}

func runUpdateTime(run storage.ExecutionRun) time.Time {
	now := time.Now().UTC()
	if now.Before(run.UpdatedAt) {
		return run.UpdatedAt
	}
	return now
}

// PruneRunActivity is explicit bounded retention, not automatic run deletion.
// Identity, snapshots, settings, phase/outcome, conversation IDs and commit
// evidence survive forever. Outstanding checkpoints therefore need no pin graph.
func (db *ProjectDatabase) PruneRunActivity(ctx context.Context, before time.Time, limit int) (count int64, err error) {
	if before.IsZero() || limit < 1 || limit > 1000 {
		return 0, storage.InvalidExecutionRun("retention requires a cutoff and limit 1..1000")
	}
	err = db.writeRun(ctx, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(ctx, `SELECT run_id FROM execution_runs WHERE finished_at < ? AND activity_pruned=0 AND pending_operation='' ORDER BY finished_at, run_id LIMIT ?`, before.UTC().Format(runTimeFormat), limit)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := conn.ExecContext(ctx, `UPDATE execution_runs SET summary='', activity_pruned=1, revision=revision+1 WHERE run_id=?`, id); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `DELETE FROM execution_run_activity WHERE run_id=?`, id); err != nil {
				return err
			}
		}
		count = int64(len(ids))
		return nil
	})
	return count, err
}

type runQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readExecutionRun(ctx context.Context, q runQuery, id int64) (run storage.ExecutionRun, err error) {
	var settings, promptPrefix, finalization, interaction, created, updated, checkpointScope string
	var finished, verified sql.NullString
	err = q.QueryRowContext(ctx, `SELECT r.run_id, r.attempt, r.revision, r.project_id, r.workspace_id, r.pellet_number,
		r.resume_from, r.mode, r.external_id, r.group_id, r.settings_json, r.prompt_prefix_json, r.starting_head, r.pellet_title, r.pellet_description,
		r.phase, r.state, r.thread_id, r.turn_id, r.outcome, r.error_code, r.summary, r.cached_input_tokens, r.finalization_json, r.result_commit,
		r.interaction_json, r.commit_verified_at, r.created_at, r.updated_at, r.finished_at, r.activity_pruned, p.code, r.pending_operation, r.pending_revision, r.pending_turn_id,
		EXISTS(SELECT 1 FROM pellets WHERE project_id=r.project_id AND number=r.pellet_number),
		w.root_path, w.root_path_relative, w.git_dir, w.git_dir_relative, p.git_common_dir, p.git_common_dir_relative, r.starting_ref, r.schedule_mode, r.schedule_remaining, r.implementation_revision, r.checkpoint_scope_json
		FROM execution_runs r JOIN projects p ON p.project_id=r.project_id
		JOIN project_workspaces w ON w.workspace_id=r.workspace_id WHERE r.run_id=?`, id).Scan(
		&run.ID, &run.Attempt, &run.Revision, &run.ProjectID, &run.WorkspaceID, &run.PelletNumber,
		&run.ResumeFrom, &run.Mode, &run.ExternalID, &run.Group, &settings, &promptPrefix, &run.StartingHead, &run.PelletTitle, &run.PelletDescription,
		&run.Phase, &run.State, &run.ThreadID, &run.TurnID, &run.Outcome, &run.ErrorCode, &run.Summary, &run.CachedInputTokens, &finalization, &run.ResultCommit,
		&interaction, &verified, &created, &updated, &finished, &run.ActivityPruned, &run.ProjectCode, &run.PendingOperation, &run.PendingRevision, &run.PendingTurnID, &run.PelletPresent,
		&run.WorkspaceRoot.Value, &run.WorkspaceRoot.Relative, &run.WorkspaceGitDir.Value, &run.WorkspaceGitDir.Relative, &run.GitCommonDir.Value, &run.GitCommonDir.Relative, &run.StartingRef, &run.ScheduleMode, &run.ScheduleRemaining, &run.ImplementationRevision, &checkpointScope)
	if errors.Is(err, sql.ErrNoRows) {
		return run, storage.ExecutionRunNotFound(id)
	}
	if err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(settings), &run.Settings); err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(checkpointScope), &run.CheckpointScope); err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(promptPrefix), &run.PromptPrefix); err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(finalization), &run.Finalization); err != nil {
		return run, err
	}
	if err := json.Unmarshal([]byte(interaction), &run.Interaction); err != nil {
		return run, err
	}
	if err := storage.ValidateRunCapture(run.RunCapture); err != nil {
		return run, err
	}
	if err := storage.ValidateRunProgress(run.RunProgress); err != nil {
		return run, err
	}
	for _, field := range []struct {
		text  string
		value *time.Time
	}{{created, &run.CreatedAt}, {updated, &run.UpdatedAt}} {
		*field.value, err = time.Parse(runTimeFormat, field.text)
		if err != nil {
			return run, err
		}
	}
	if finished.Valid {
		t, err := time.Parse(runTimeFormat, finished.String)
		if err != nil {
			return run, err
		}
		run.FinishedAt = &t
	}
	if verified.Valid {
		t, err := time.Parse(runTimeFormat, verified.String)
		if err != nil {
			return run, err
		}
		run.CommitVerifiedAt = &t
	}
	rows, err := q.QueryContext(ctx, `SELECT revision, at, phase, state, summary, operation FROM execution_run_activity WHERE run_id=? ORDER BY revision DESC LIMIT ?`, id, storage.MaxRunActivity)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	run.Activity = []storage.RunActivity{}
	for rows.Next() {
		var a storage.RunActivity
		var at string
		if err := rows.Scan(&a.Revision, &at, &a.Phase, &a.State, &a.Summary, &a.Operation); err != nil {
			return run, err
		}
		a.At, err = time.Parse(runTimeFormat, at)
		if err != nil {
			return run, err
		}
		run.Activity = append(run.Activity, a)
	}
	return run, rows.Err()
}

func appendRunActivity(ctx context.Context, conn *sql.Conn, id, revision int64, at string, p storage.RunProgress, operations ...string) error {
	operation := ""
	if len(operations) > 0 {
		operation = operations[0]
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO execution_run_activity(run_id, revision, at, phase, state, summary, operation) VALUES(?,?,?,?,?,?,?)`, id, revision, at, p.Phase, p.State, p.Summary, operation); err != nil {
		return err
	}
	_, err := conn.ExecContext(ctx, `DELETE FROM execution_run_activity WHERE run_id=? AND revision NOT IN (SELECT revision FROM execution_run_activity WHERE run_id=? ORDER BY revision DESC LIMIT ?)`, id, id, storage.MaxRunActivity)
	return err
}

func (db *ProjectDatabase) writeRun(ctx context.Context, write func(*sql.Conn) error) error {
	conn, err := db.db.Conn(ctx)
	if err != nil {
		return runStorageError(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return runStorageError(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err := write(conn); err != nil {
		return runStorageError(err)
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return runStorageError(err)
}

func runStorageError(err error) error {
	if err == nil {
		return nil
	}
	var known *domain.Error
	if errors.As(err, &known) {
		return err
	}
	if stable := stableDatabaseError("access execution evidence", err); stable != nil {
		return stable
	}
	return domain.WrapError(domain.Storage, "execution_run_storage_failed", "could not access durable execution evidence", nil, fmt.Errorf("execution records: %w", err))
}
