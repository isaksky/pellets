package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
)

func preflightRecoveryRequired() error {
	return scheduleError("workspace_execution_recovery_required", "the preflight recovery receipt is legacy, ambiguous, or belongs to different work; preserve it and reconcile the exact workspace before continuing")
}

func capturePreflight(ctx context.Context, request ExecutionRequest, root string) (*executionlock.Preflight, error) {
	identity, err := discovery.FindGitIdentity(ctx, root)
	if err != nil {
		return nil, err
	}
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !storage.IsFullCommitID(head) {
		return nil, errors.Join(missingRunEvidence("starting_head_unavailable"), err)
	}
	ref, err := executionGit(ctx, root, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil || ref == "" {
		return nil, missingRunEvidence("starting_ref_unavailable")
	}
	databaseIdentity, err := executionlock.FileIdentity(request.Database.Path)
	if err != nil {
		return nil, err
	}
	gitIdentity, err := executionlock.FileIdentity(identity.GitDir)
	if err != nil {
		return nil, err
	}
	c := request.Capture
	return &executionlock.Preflight{Version: 1, Platform: runtime.GOOS, DatabaseIdentity: databaseIdentity, GitIdentity: gitIdentity,
		ProjectID: c.ProjectID, WorkspaceID: c.WorkspaceID, PelletNumber: c.PelletNumber, ImplementationRevision: c.ExpectedImplementationRevision,
		Root: root, GitDir: identity.GitDir, GitCommonDir: identity.GitCommonDir, StartingHead: head, StartingRef: ref,
		Mode: c.Mode, ScheduleMode: c.ScheduleMode, ScheduleRemaining: c.ScheduleRemaining, ExternalID: c.ExternalID, Group: c.Group}, nil
}

func matchPreflight(previous, current *executionlock.Preflight) error {
	if previous == nil || previous.Version != 1 || previous.Platform != runtime.GOOS || previous.DatabaseIdentity == "" || previous.GitIdentity == "" || previous.ImplementationRevision < 1 {
		return preflightRecoveryRequired()
	}
	return samePreflight(previous, current)
}

func samePreflight(previous, current *executionlock.Preflight) error {
	if !reflect.DeepEqual(previous, current) {
		return preflightRecoveryRequired()
	}
	return nil
}

// PreflightRecovery is display-only. A valid snapshot does not prove cleanup;
// explicit admission must acquire the OS lock and revalidate it again.
func (supervisor *ExecutionSupervisor) PreflightRecovery(ctx context.Context, database Database, selected storage.ResolvedProject, pellet storage.Pellet) (*executionlock.Preflight, error) {
	gitDir, err := discovery.ResolveLocalPath(database.Root, selected.Workspace.GitDir)
	if err != nil {
		return nil, err
	}
	owner, err := executionlock.ReadOwner(gitDir)
	if err != nil {
		return nil, preflightRecoveryRequired()
	}
	if owner == nil {
		return nil, nil
	}
	if runtime.GOOS == "windows" {
		return nil, scheduleError("process_cleanup_unconfirmed", "previous process cleanup cannot be verified on this platform; preserve the recovery receipt")
	}
	if owner.Database != database.Path || owner.RunID != 0 || owner.Preflight == nil {
		return nil, preflightRecoveryRequired()
	}
	p := owner.Preflight
	root, err := executionRoot(ctx, database, storage.ExecutionRun{WorkspaceRoot: selected.Workspace.RootPath, WorkspaceGitDir: selected.Workspace.GitDir, GitCommonDir: selected.Project.GitCommonDir})
	if err != nil {
		return nil, err
	}
	if pellet.Status != domain.PelletInProgress || pellet.Workspace == nil || pellet.Workspace.ID != selected.Workspace.ID || !storage.MatchesSchedule(pellet, p.ExternalID, p.Group) {
		return nil, preflightRecoveryRequired()
	}
	if p.ScheduleMode != "run_one" && p.ScheduleMode != "drain" && p.ScheduleMode != "watch" || p.ScheduleRemaining < 1 || p.ScheduleRemaining > 10000 {
		return nil, preflightRecoveryRequired()
	}
	mode := p.ScheduleMode
	if pellet.Kind == domain.PelletReviewCheckpoint {
		mode = "review_checkpoint"
	}
	current, err := capturePreflight(ctx, ExecutionRequest{Database: database, Selected: selected, Capture: storage.RunCapture{ProjectID: selected.Project.ID, WorkspaceID: selected.Workspace.ID, PelletNumber: pellet.Reference.Number, ExpectedImplementationRevision: pellet.ImplementationRevision, Mode: mode, ScheduleMode: p.ScheduleMode, ScheduleRemaining: p.ScheduleRemaining, ExternalID: p.ExternalID, Group: p.Group}}, root)
	if err != nil {
		return nil, err
	}
	if err := matchPreflight(p, current); err != nil {
		return nil, err
	}
	return p, nil
}

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
		if owner.Database != request.Database.Path || owner.RunID != previous.ID {
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
		head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return previous, err
		}
		if err := checkResumeHead(ctx, root, previous, head); err != nil {
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
	if request.Capture.FreshConversation && (!storage.ResumeUsesCurrentHead(previous) || storage.RunActive(previous.State) || previous.PendingOperation != "") {
		return previous, scheduleError("fresh_conversation_unavailable", "The previous operation must finish or be reconciled before starting a fresh conversation.")
	}
	if !request.Capture.FreshConversation && previous.ThreadID == "" && (previous.PendingOperation != "" || previous.Phase != "preflight") {
		return previous, scheduleError("resume_conversation_unidentified", "the previous thread creation is unconfirmed; locate its Codex history before continuing, without creating another conversation")
	}
	if storage.RunActive(previous.State) && !request.admissionOnly {
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
	previousTurnStatus := ""
	for _, turn := range result.Thread.Turns {
		if turn.ID == "" || turn.Status != "completed" && turn.Status != "interrupted" && turn.Status != "failed" {
			return scheduleError("resume_turn_unconfirmed", "a saved turn has no terminal outcome; inspect Codex history before continuing")
		}
		last = turn.ID
		if turn.ID == previous.TurnID {
			found = true
			previousTurnStatus = turn.Status
		}
	}
	if !found {
		return scheduleError("resume_turn_missing", "the recorded turn is missing from Codex history; restore it before Resume")
	}
	if previous.Mode == "review_checkpoint" && previous.ReviewResult != nil && previousTurnStatus != "completed" {
		return scheduleError("review_resume_turn_unsuccessful", "the saved reviewer result does not have a successfully completed terminal turn")
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

// Ordinary implementation resumes from the current repository. Old HEAD is
// evidence for the old attempt; finalization and reviews still bind exact work.
func checkResumeHead(ctx context.Context, root string, previous storage.ExecutionRun, head string) error {
	if head == previous.StartingHead {
		return nil
	}
	if !storage.ResumeUsesCurrentHead(previous) {
		return missingRunEvidence("implementation_head_changed")
	}
	return nil
}
