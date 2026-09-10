package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
)

// RecoveryPending is display-only. Admission still obtains the OS lock and
// validates the exact receipt; seeing bytes never proves process cleanup.
func (supervisor *ExecutionSupervisor) RecoveryPending(database Database, run storage.ExecutionRun) bool {
	gitDir, err := discovery.ResolveLocalPath(database.Root, run.WorkspaceGitDir)
	if err != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(gitDir, "pellets-execution.lock"))
	return err == nil && info.Size() != 0
}

// ReconcileStartup changes evidence only. It never prepares Codex, selects a
// pellet, clears a receipt, or recreates an in-memory Watch/Drain schedule.
// A busy OS lock belongs to a participating live server/custodian and is left
// alone. Missing worktrees retain their saved identities and require attention.
func (supervisor *ExecutionSupervisor) ReconcileStartup(ctx context.Context, database Database, projects []storage.WebProjectSummary) error {
	for _, project := range projects {
		for _, workspace := range project.Project.Workspaces {
			runs, err := supervisor.options.Recorder.ListWorkspaceRuns(ctx, database, workspace.ID, 1)
			if err != nil {
				return err
			}
			if len(runs) == 0 || !storage.RunActive(runs[0].State) {
				continue
			}
			run := runs[0]
			root, rootErr := executionRoot(ctx, database, run)
			var lock *executionlock.Lock
			if rootErr == nil {
				identity, err := discovery.FindGitIdentity(ctx, root)
				if err != nil {
					rootErr = err
				} else {
					lock, rootErr = executionlock.AcquireRecovery(identity.GitDir)
				}
				if domain.PublicError(rootErr).Code == "workspace_execution_busy" {
					continue
				}
			}
			if lock != nil && !lock.RecoveryStopped() {
				rootErr = scheduleError("process_cleanup_unconfirmed", "previous owned process cleanup cannot yet be confirmed on this platform")
			}
			if rootErr != nil {
				progress := run.RunProgress
				progress.State, progress.Outcome, progress.ErrorCode = "needs_attention", "unknown", domain.PublicError(rootErr).Code
				progress.Interaction = nil
				progress.Summary = "Server restarted. The saved workspace or process cleanup needs attention; preserve this attempt and its conversation."
				_, err = supervisor.options.Recorder.Save(ctx, database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
			} else {
				_, err = supervisor.options.Recorder.MarkInterrupted(ctx, database, run.ID, run.Revision)
			}
			if lock != nil {
				err = errors.Join(err, lock.Close())
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (supervisor *ExecutionSupervisor) validateResume(ctx context.Context, request ExecutionRequest, root string, lock *executionlock.Lock) (storage.ExecutionRun, error) {
	previous, err := supervisor.options.Recorder.Read(ctx, request.Database, *request.Capture.ResumeFrom)
	if err != nil {
		return previous, err
	}
	if previous.ProjectID != request.Capture.ProjectID || previous.WorkspaceID != request.Capture.WorkspaceID || previous.State == "completed" && lock.Owner() == nil {
		return previous, storage.ExecutionRunConflict(previous.ID)
	}
	latest, err := supervisor.options.Recorder.ListWorkspaceRuns(ctx, request.Database, previous.WorkspaceID, 1)
	if err != nil {
		return previous, err
	}
	if len(latest) != 1 || latest[0].ID != previous.ID {
		return previous, storage.ExecutionRunConflict(previous.ID)
	}
	if owner := lock.Owner(); owner != nil {
		if owner.Database != request.Database.Path || owner.RunID != 0 && owner.RunID != previous.ID {
			return previous, scheduleError("workspace_execution_recovery_required", "the recovery receipt belongs to another database or attempt; inspect that exact receipt")
		}
		if !lock.RecoveryStopped() {
			return previous, scheduleError("process_cleanup_unconfirmed", "this version cannot inspect a previous unnamed Windows Job Object after a crash; automatic Resume is unavailable for this receipt. The work and conversation are preserved. Do not delete the lock file")
		}
	}
	if _, err := executionRoot(ctx, request.Database, previous); err != nil {
		return previous, err
	}
	if previous.StartingRef == "" {
		return previous, scheduleError("starting_ref_unavailable", "this older attempt did not capture its branch; preserve the work and reconcile the branch manually before continuing outside automatic recovery")
	}
	ref, err := executionGit(ctx, root, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil || ref != previous.StartingRef {
		return previous, scheduleError("resume_branch_changed", "the worktree branch differs from the saved attempt; restore the original branch after reviewing local changes, then use Resume")
	}
	if previous.Finalization == nil {
		if err := requireHead(ctx, root, previous.StartingHead); err != nil {
			return previous, err
		}
	} else {
		head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return previous, err
		}
		if previous.ResultCommit != "" && previous.ResultCommit != head {
			return previous, missingRunEvidence("result_commit_not_head")
		}
		if head != previous.StartingHead {
			if err := validateFinalizationCommit(ctx, root, previous, head); err != nil {
				return previous, err
			}
		}
	}
	if previous.ThreadID == "" && (previous.PendingOperation != "" || previous.Phase != "preflight") {
		return previous, scheduleError("resume_conversation_unidentified", "the previous thread creation is unconfirmed; locate its Codex history before continuing, without creating another conversation")
	}
	if storage.RunActive(previous.State) {
		previous, err = supervisor.options.Recorder.MarkInterrupted(ctx, request.Database, previous.ID, previous.Revision)
	}
	return previous, err
}

// Reading full turns is a stable installed-runtime API and never loads or
// starts a thread. Responses stay in memory. Only exact orchestration identities
// from an unambiguously stopped history may settle a pending durable call.
func (supervisor *ExecutionSupervisor) reconcileConversation(ctx context.Context, request ExecutionRequest, root string, session codex.Session) error {
	previous, err := supervisor.options.Recorder.Read(ctx, request.Database, *request.Capture.ResumeFrom)
	if err != nil || previous.ThreadID == "" {
		return err
	}
	response, err := session.Call(ctx, codex.ThreadRead, map[string]any{"threadId": previous.ThreadID, "includeTurns": true})
	var result struct {
		Thread struct {
			ID     string `json:"id"`
			Cwd    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
			Turns []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err != nil || json.Unmarshal(response, &result) != nil || result.Thread.ID != previous.ThreadID || result.Thread.Cwd == "" || result.Thread.Turns == nil {
		return scheduleError("resume_history_unavailable", "the exact saved Codex conversation is unavailable or incomplete; restore its history before Resume")
	}
	cwd, err := filepath.EvalSymlinks(result.Thread.Cwd)
	if err != nil || cwd != root {
		return scheduleError("resume_history_workspace_changed", "the saved conversation belongs to a different or unavailable worktree")
	}
	switch result.Thread.Status.Type {
	case "idle", "notLoaded":
	default:
		return scheduleError("resume_history_not_stopped", "Codex history does not establish a stopped conversation; inspect its active or error state")
	}
	last := ""
	found := previous.TurnID == ""
	for _, turn := range result.Thread.Turns {
		if turn.ID == "" || turn.Status != "completed" && turn.Status != "interrupted" && turn.Status != "failed" {
			return scheduleError("resume_turn_unconfirmed", "a saved turn has no terminal outcome; inspect Codex history before continuing")
		}
		last = turn.ID
		if turn.ID == previous.TurnID {
			found = true
		}
	}
	if !found {
		return scheduleError("resume_turn_missing", "the recorded turn is missing from Codex history; restore it before Resume")
	}
	// A phase can outlive its settled call. Only an actual pending turn start
	// permits a new history identity; no saved turn means history must be empty.
	if previous.PendingOperation != "turn/start" && last != previous.TurnID {
		return scheduleError("resume_history_advanced", "the conversation has newer turns than this attempt; reconcile the external work first")
	}
	if previous.PendingOperation == "" {
		return nil
	}
	completion := storage.ExecutionOperationResult{ID: previous.ID, PendingRevision: previous.PendingRevision}
	switch previous.PendingOperation {
	case "thread/resume":
		completion.ThreadID = previous.ThreadID
	case "turn/interrupt":
	case "turn/start":
		previousIndex := -1
		for index, turn := range result.Thread.Turns {
			if turn.ID == previous.PendingTurnID {
				previousIndex = index
			}
		}
		if last == "" || last == previous.PendingTurnID || previousIndex+2 != len(result.Thread.Turns) {
			return scheduleError("resume_call_unconfirmed", "the pending turn start has no unique result in history; do not replay it")
		}
		completion.ThreadID, completion.TurnID = previous.ThreadID, last
	default:
		return scheduleError("resume_call_unconfirmed", "the pending Codex call cannot be reconciled from the saved identities")
	}
	db, err := supervisor.options.Recorder.repository(ctx, request.Database)
	if err != nil {
		return err
	}
	_, err = db.FinishExecutionOperation(ctx, completion)
	return errors.Join(err, db.Close())
}
