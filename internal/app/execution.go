package app

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

type ExecutionRunDatabaseOpener func(context.Context, string) (storage.ExecutionRunDatabase, error)

// ExecutionRecorder is an internal foreground-supervisor boundary. It neither
// selects pellets nor resumes work on open. Callers must stop on persistence
// failure; an unsuccessful final save never proves the external action failed.
type ExecutionRecorder struct{ Open ExecutionRunDatabaseOpener }

func (recorder ExecutionRecorder) repository(ctx context.Context, database Database) (storage.ExecutionRunDatabase, error) {
	if recorder.Open == nil {
		return nil, domain.NewError(domain.Unexpected, "internal_error", "execution recorder is not configured", nil)
	}
	return recorder.Open(ctx, database.Path)
}

// Begin captures HEAD before starting a thread or turn. The selected project
// and workspace must already have been resolved through the normal binding.
func (recorder ExecutionRecorder) Begin(ctx context.Context, database Database, selected storage.ResolvedProject, capture storage.RunCapture) (storage.ExecutionRun, error) {
	if selected.Project.ID != capture.ProjectID || selected.Workspace.ID != capture.WorkspaceID || selected.Workspace.ProjectID != capture.ProjectID {
		return storage.ExecutionRun{}, storage.InvalidExecutionRun("run does not match the resolved workspace")
	}
	run := storage.ExecutionRun{WorkspaceRoot: selected.Workspace.RootPath, WorkspaceGitDir: selected.Workspace.GitDir, GitCommonDir: selected.Project.GitCommonDir}
	root, err := executionRoot(ctx, database, run)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !storage.IsFullCommitID(head) {
		return storage.ExecutionRun{}, errors.Join(missingRunEvidence("starting_head_unavailable"), err)
	}
	ref, err := executionGit(ctx, root, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil || ref == "" {
		return storage.ExecutionRun{}, missingRunEvidence("starting_ref_unavailable")
	}
	if capture.ExpectedImplementationRevision > 0 && capture.ResumeFrom == nil && (capture.StartingHead != "" && capture.StartingHead != head || capture.StartingRef != "" && capture.StartingRef != ref) {
		return storage.ExecutionRun{}, scheduleError("preflight_repository_changed", "the repository HEAD or branch changed during preflight; preserve the work and inspect the recovery receipt")
	}
	capture.StartingHead, capture.StartingRef = head, ref
	if capture.ResumeFrom != nil {
		previous, err := recorder.Read(ctx, database, *capture.ResumeFrom)
		if err != nil {
			return storage.ExecutionRun{}, err
		}
		if previous.Finalization == nil {
			if err := checkResumeHead(ctx, root, previous, head); err != nil {
				return storage.ExecutionRun{}, err
			}
		}
		if previous.StartingRef == "" || previous.StartingRef != capture.StartingRef {
			return storage.ExecutionRun{}, scheduleError("resume_branch_changed", "the branch changed during Resume preparation; restore and reconcile the original branch before continuing")
		}
	}
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	capture.ExpectedWorkspace = &selected
	created, operationErr := repo.CreateExecutionRun(ctx, capture)
	return created, errors.Join(operationErr, repo.Close())
}

func (recorder ExecutionRecorder) Read(ctx context.Context, database Database, id int64) (storage.ExecutionRun, error) {
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	run, operationErr := repo.ReadExecutionRun(ctx, id)
	return run, errors.Join(operationErr, repo.Close())
}

// ListWorkspaceRuns returns durable, bounded run evidence for one explicitly
// identified workspace. It is read-only and deliberately does not reconcile,
// resume, or otherwise alter an attempt.
func (recorder ExecutionRecorder) ListWorkspaceRuns(ctx context.Context, database Database, workspaceID int64, limit int) ([]storage.ExecutionRun, error) {
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return nil, err
	}
	runs, operationErr := repo.ListWorkspaceRuns(ctx, workspaceID, 0, limit)
	return runs, errors.Join(operationErr, repo.Close())
}

func (recorder ExecutionRecorder) Save(ctx context.Context, database Database, request storage.UpdateExecutionRun) (storage.ExecutionRun, error) {
	// Verified commit evidence is available only through VerifyCommit below.
	if request.VerifiedCommit != "" {
		return storage.ExecutionRun{}, storage.InvalidExecutionRun("use local commit verification before recording commit evidence")
	}
	return recorder.save(ctx, database, request)
}

func (recorder ExecutionRecorder) CompleteReviewCheckpoint(ctx context.Context, database Database, id, revision int64) (storage.ExecutionRun, error) {
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	run, operationErr := repo.CompleteReviewCheckpoint(ctx, id, revision)
	return run, errors.Join(operationErr, repo.Close())
}

// MarkInterrupted is used only after the foreground supervisor is known to
// have stopped. Opening a database or listing records never invokes it. The
// last durable phase and conversation IDs survive for explicit reconciliation.
func (recorder ExecutionRecorder) MarkInterrupted(ctx context.Context, database Database, id, revision int64) (storage.ExecutionRun, error) {
	return recorder.markInterrupted(ctx, database, id, revision, "unknown")
}

func (recorder ExecutionRecorder) markInterrupted(ctx context.Context, database Database, id, revision int64, outcome string) (storage.ExecutionRun, error) {
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	run, err := repo.InterruptExecutionRun(ctx, id, revision, outcome)
	return run, errors.Join(err, repo.Close())
}

func (recorder ExecutionRecorder) save(ctx context.Context, database Database, request storage.UpdateExecutionRun) (storage.ExecutionRun, error) {
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	run, operationErr := repo.UpdateExecutionRun(ctx, request)
	return run, errors.Join(operationErr, repo.Close())
}

// CallCodex writes intent before a consequential app-server call, then records
// returned identities immediately. Parameters and responses remain outside
// SQLite. A crash between call and save leaves the precise intent unresolved;
// reconnect must inspect that attempt, never blindly replay the operation.
func (recorder ExecutionRecorder) CallCodex(ctx context.Context, database Database, id, revision int64, session codex.Session, operation codex.Operation, params map[string]any) (storage.ExecutionRun, json.RawMessage, error) {
	run, err := recorder.Read(ctx, database, id)
	if err != nil {
		return run, nil, err
	}
	if session == nil {
		return run, nil, storage.InvalidExecutionRun("Codex session is required")
	}
	progress := run.RunProgress
	progress.Summary = ""
	switch operation {
	case codex.ThreadStart:
		if run.ThreadID != "" {
			return run, nil, storage.ExecutionRunConflict(id)
		}
		progress.Phase = "thread_start"
	case codex.ThreadResume:
		if run.ThreadID == "" || params["threadId"] != run.ThreadID {
			return run, nil, storage.ExecutionRunConflict(id)
		}
		progress.Phase = "thread_start"
	case codex.TurnStart:
		if run.ThreadID == "" || params["threadId"] != run.ThreadID {
			return run, nil, storage.ExecutionRunConflict(id)
		}
		progress.Phase = "turn_start"
	case codex.ReviewStart:
		if run.ThreadID == "" || run.CheckpointScope == nil || params["threadId"] != run.ThreadID {
			return run, nil, storage.ExecutionRunConflict(id)
		}
		progress.Phase = "review"
	case codex.TurnInterrupt:
		if run.TurnID == "" || params["threadId"] != run.ThreadID || params["turnId"] != run.TurnID {
			return run, nil, storage.ExecutionRunConflict(id)
		}
		// An interrupt request is not proof the turn has stopped.
	default:
		return run, nil, storage.InvalidExecutionRun("operation is outside the recorded conversation transition boundary")
	}
	repo, err := recorder.repository(ctx, database)
	if err != nil {
		return run, nil, err
	}
	run, err = repo.BeginExecutionOperation(ctx, id, revision, string(operation))
	err = errors.Join(err, repo.Close())
	if err != nil {
		return run, nil, err
	}
	response, callErr := session.Call(ctx, operation, params)
	completion := storage.ExecutionOperationResult{ID: id, PendingRevision: run.PendingRevision}
	if callErr == nil && operation != codex.TurnInterrupt {
		var result struct {
			ReviewThreadID string `json:"reviewThreadId"`
			Thread         struct {
				ID string `json:"id"`
			} `json:"thread"`
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(response, &result) != nil {
			callErr = codex.ErrProtocol
		} else {
			switch operation {
			case codex.ThreadStart, codex.ThreadResume:
				if result.Thread.ID == "" || (progress.ThreadID != "" && result.Thread.ID != progress.ThreadID) {
					callErr = codex.ErrProtocol
				} else {
					progress.ThreadID = result.Thread.ID
				}
			case codex.TurnStart:
				if result.Turn.ID == "" {
					callErr = codex.ErrProtocol
				} else {
					progress.TurnID = result.Turn.ID
				}
			case codex.ReviewStart:
				if result.ReviewThreadID == "" || result.ReviewThreadID == run.ThreadID || result.Turn.ID == "" {
					callErr = codex.ErrProtocol
				} else {
					progress.ThreadID, progress.TurnID = result.ReviewThreadID, result.Turn.ID
				}
			}
		}
	}
	if callErr == nil {
		callErr = storage.ValidateRunProgress(progress)
	}
	if callErr != nil {
		completion.ErrorCode = "codex_call_unconfirmed"
		completion.Summary = executionFailureDiagnostic(callErr)
	} else {
		switch operation {
		case codex.ThreadStart, codex.ThreadResume:
			completion.ThreadID = progress.ThreadID
		case codex.TurnStart, codex.ReviewStart:
			completion.ThreadID, completion.TurnID = progress.ThreadID, progress.TurnID
		}
	}
	// A cancelled request must still make a bounded best effort to persist its
	// uncertain outcome. Only a bounded, sanitized RPC message may enter the durable summary.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	repo, saveErr := recorder.repository(saveCtx, database)
	if saveErr != nil {
		return run, response, errors.Join(callErr, saveErr)
	}
	run, saveErr = repo.FinishExecutionOperation(saveCtx, completion)
	saveErr = errors.Join(saveErr, repo.Close())
	return run, response, errors.Join(callErr, saveErr)
}

// VerifyCommit checks the exact object, HEAD, and ancestry before retaining
// commit evidence. This does not commit, close a pellet, or claim test success.
func (recorder ExecutionRecorder) VerifyCommit(ctx context.Context, database Database, id, revision int64, commit string) (storage.ExecutionRun, error) {
	run, err := recorder.Read(ctx, database, id)
	if err != nil {
		return run, err
	}
	if run.Revision != revision {
		return run, storage.ExecutionRunConflict(id)
	}
	if !storage.IsFullCommitID(commit) {
		return run, storage.InvalidExecutionRun("commit must be a full immutable object ID")
	}
	root, err := executionRoot(ctx, database, run)
	if err != nil {
		return run, err
	}
	if err := verifyRunCommit(ctx, root, commit); err != nil {
		return run, err
	}
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != commit {
		return run, errors.Join(missingRunEvidence("result_commit_not_head"), err)
	}
	if _, err := executionGit(ctx, root, "merge-base", "--is-ancestor", run.StartingHead, commit); err != nil {
		return run, errors.Join(missingRunEvidence("starting_head_not_ancestor"), err)
	}
	progress := run.RunProgress
	if progress.Phase != "close" {
		progress.Phase = "finalization"
	}
	return recorder.save(ctx, database, storage.UpdateExecutionRun{ID: id, ExpectedRevision: revision, Progress: progress, VerifiedCommit: commit})
}

type RunEvidence struct {
	Run            storage.ExecutionRun `json:"run"`
	StartingCommit string               `json:"starting_commit"`
	ResultCommit   string               `json:"result_commit"`
	Conversation   string               `json:"conversation"`
}

// InspectEvidence always addresses a stored run ID. Availability is checked
// separately from its historical outcome: success never implies an extant
// transcript or Git object. ThreadRead excludes turns and is not persisted.
func (recorder ExecutionRecorder) InspectEvidence(ctx context.Context, database Database, id int64, session codex.Session) (RunEvidence, error) {
	run, err := recorder.Read(ctx, database, id)
	if err != nil {
		return RunEvidence{}, err
	}
	evidence := RunEvidence{Run: run, StartingCommit: "unavailable", ResultCommit: "not_recorded", Conversation: "not_recorded"}
	root, rootErr := executionRoot(ctx, database, run)
	if rootErr == nil && verifyRunCommit(ctx, root, run.StartingHead) == nil {
		evidence.StartingCommit = "available"
	}
	if run.ResultCommit != "" {
		evidence.ResultCommit = "unavailable"
		if rootErr == nil && verifyRunCommit(ctx, root, run.ResultCommit) == nil {
			evidence.ResultCommit = "available"
		}
	}
	if run.ThreadID != "" {
		evidence.Conversation = "unchecked"
		if session != nil {
			evidence.Conversation = "unavailable"
			response, readErr := session.Call(ctx, codex.ThreadRead, map[string]any{"threadId": run.ThreadID, "includeTurns": false})
			var result struct {
				Thread struct {
					ID string `json:"id"`
				} `json:"thread"`
			}
			if readErr == nil && json.Unmarshal(response, &result) == nil && result.Thread.ID == run.ThreadID {
				evidence.Conversation = "available"
			}
		}
	}
	return evidence, nil
}

func executionRoot(ctx context.Context, database Database, run storage.ExecutionRun) (string, error) {
	root, err := discovery.ResolveLocalPath(database.Root, run.WorkspaceRoot)
	if err != nil {
		return "", missingRunEvidence("workspace_unavailable")
	}
	identity, err := discovery.FindGitIdentity(ctx, root)
	if err != nil {
		return "", missingRunEvidence("workspace_unavailable")
	}
	for _, pair := range []struct {
		actual string
		stored domain.LocalPath
	}{{identity.WorkTreeRoot, run.WorkspaceRoot}, {identity.GitDir, run.WorkspaceGitDir}, {identity.GitCommonDir, run.GitCommonDir}} {
		path, err := discovery.NormalizeLocalPath(database.Root, pair.actual)
		if err != nil || path != pair.stored {
			return "", missingRunEvidence("workspace_identity_changed")
		}
	}
	return root, nil
}

func verifyRunCommit(ctx context.Context, root, commit string) error {
	if !storage.IsFullCommitID(commit) {
		return missingRunEvidence("commit_not_recorded")
	}
	objectType, err := executionGit(ctx, root, "cat-file", "-t", commit)
	if err != nil || objectType != "commit" {
		return errors.Join(missingRunEvidence("commit_unavailable"), err)
	}
	return nil
}

func executionGit(ctx context.Context, root string, args ...string) (string, error) {
	output, err := executionGitRaw(ctx, root, args...)
	return strings.TrimSpace(output), err
}

func executionGitRaw(ctx context.Context, root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"--no-replace-objects", "-C", root}, args...)...)
	output, diagnostic, err := codex.RunOwnedCommand(ctx, command)
	if err != nil {
		return output, newExecutionGitFailure(err, diagnostic)
	}
	return output, nil
}

func missingRunEvidence(code string) error {
	return domain.NewError(domain.Conflict, code, "the exact execution evidence is unavailable; do not substitute current work or treat this as success", nil)
}
