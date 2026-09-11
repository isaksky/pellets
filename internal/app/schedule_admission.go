package app

import (
	"context"
	"errors"
	"os"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
)

// CheckAdmission performs the button's checks without reserving a pellet,
// recording an attempt, or creating a Codex conversation. Execution rechecks
// mutable state under its own lock; passing a check is not a lease on that state.
func (s *Scheduler) CheckAdmission(ctx context.Context, request ScheduleRequest) (resultErr error) {
	supervisor := s.options.Supervisor
	if supervisor == nil {
		return scheduleError("scheduler_unavailable", "Scheduling is unavailable.")
	}
	supervisor.mu.Lock()
	if supervisor.stopping {
		supervisor.mu.Unlock()
		return scheduleError("server_stopping", "The server is stopping.")
	}
	ctx, cancel := context.WithCancel(ctx)
	supervisor.nextAdmission++
	admissionID := supervisor.nextAdmission
	if supervisor.admissions == nil {
		supervisor.admissions = make(map[uint64]context.CancelFunc)
	}
	supervisor.admissions[admissionID] = cancel
	supervisor.wait.Add(1)
	supervisor.mu.Unlock()
	defer func() {
		cancel()
		supervisor.mu.Lock()
		delete(supervisor.admissions, admissionID)
		supervisor.mu.Unlock()
		supervisor.wait.Done()
	}()
	if request.Selected.Workspace.ID < 1 || request.Selected.Project.ID != request.Selected.Workspace.ProjectID {
		return storage.InvalidExecutionRun("choose an existing registered workspace")
	}
	if request.ResumeFrom != nil && request.ResumePellet == nil {
		return storage.InvalidExecutionRun("Resume requires an exact pellet")
	}
	if request.Limit < 0 || request.Limit > 10000 {
		return storage.InvalidExecutionRun("invalid schedule limit")
	}
	if request.Mode != "run_one" && request.Mode != "drain" && request.Mode != "watch" {
		return storage.InvalidExecutionRun("choose run_one, drain, or watch")
	}
	s.mu.Lock()
	busy, stopping := s.workspaces[request.Selected.Workspace.ID] != nil, s.stopping
	s.mu.Unlock()
	if busy {
		return scheduleError("workspace_schedule_busy", "This workspace already has an active schedule.")
	}
	if stopping {
		return scheduleError("server_stopping", "The server is stopping.")
	}
	root, err := executionRoot(ctx, s.options.Database, storage.ExecutionRun{WorkspaceRoot: request.Selected.Workspace.RootPath, WorkspaceGitDir: request.Selected.Workspace.GitDir, GitCommonDir: request.Selected.Project.GitCommonDir})
	if err != nil {
		return err
	}
	identity, err := discovery.FindGitIdentity(ctx, root)
	if err != nil {
		return err
	}
	var lock *executionlock.Lock
	if request.ResumeFrom != nil || request.ResumePellet != nil {
		lock, err = executionlock.AcquireRecovery(identity.GitDir)
	} else {
		lock, err = executionlock.Acquire(identity.GitDir)
	}
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	ctx = codex.WithExecutionLock(ctx, lock.File())
	executionRequest := ExecutionRequest{admissionOnly: true, Database: s.options.Database, Selected: request.Selected, ResumePellet: request.ResumePellet, Capture: storage.RunCapture{ProjectID: request.Selected.Project.ID, WorkspaceID: request.Selected.Workspace.ID, ResumeFrom: request.ResumeFrom, FreshConversation: request.FreshConversation}}
	var previous *storage.ExecutionRun
	if request.ResumeFrom != nil {
		prior, err := supervisor.validateResume(ctx, executionRequest, root, lock)
		if err != nil {
			return err
		}
		if prior.PelletNumber != *request.ResumePellet {
			return storage.ExecutionRunConflict(prior.ID)
		}
		previous = &prior
		if prior.Mode == "review_checkpoint" {
			request.Overrides.Model = &prior.Settings.Codex.Model
			request.Overrides.ReasoningEffort = &prior.Settings.Codex.ReasoningEffort
		}
		request.ExternalID, request.Group = prior.ExternalID, prior.Group
	} else if request.FreshConversation {
		return storage.InvalidExecutionRun("a fresh conversation requires an exact saved attempt")
	}
	if request.ResumeFrom == nil && lock.Owner() != nil {
		owner := lock.Owner()
		if request.ResumePellet == nil || owner.RunID != 0 || owner.Preflight == nil || owner.Database != s.options.Database.Path {
			return preflightRecoveryRequired()
		}
		if !lock.RecoveryStopped() {
			return scheduleError("process_cleanup_unconfirmed", "The previous process has not finished cleanup.")
		}
		if request.PreflightReceipt != "" && request.PreflightReceipt != owner.Preflight.Token() {
			return preflightRecoveryRequired()
		}
	}
	queue, err := s.options.OpenQueue(ctx, s.options.Database.Path)
	if err != nil {
		return err
	}
	candidate := errors.New("candidate available")
	if previous != nil && previous.Finalization != nil && previous.ResultCommit != "" && (previous.Phase == "close" || previous.State == "completed") {
		_, err = queue.ReadPellet(ctx, request.Selected, domain.PelletReference{ProjectCode: previous.ProjectCode, Number: previous.PelletNumber})
	} else {
		_, err = queue.SelectScheduledPellet(ctx, request.Selected, storage.ScheduleSelection{ResumePellet: request.ResumePellet, ExternalID: request.ExternalID, Group: request.Group, Ready: func(ctx context.Context, pellet storage.Pellet) (bool, error) {
			if pellet.Kind == domain.PelletReviewCheckpoint && (s.options.Checkpoints == nil || s.options.Checkpoints.Drive == nil) {
				return false, scheduleError("checkpoint_policy_required", "Checkpoint execution is unavailable.")
			}
			if s.options.Ready != nil {
				ready, err := s.options.Ready(ctx, request.Selected, pellet)
				if err != nil || !ready {
					return ready, err
				}
			}
			return false, candidate
		}})
	}
	closeErr := queue.Close()
	if err != nil && !errors.Is(err, candidate) {
		return errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if err == nil && request.ResumeFrom == nil {
		return nil
	} // Empty queue needs no runtime.
	probe, err := os.CreateTemp(root, ".pellets-write-check-")
	if err != nil {
		return scheduleError("workspace_not_writable", "The workspace is not writable. Fix its permissions and retry.")
	}
	probePath := probe.Name()
	if err = errors.Join(probe.Close(), os.Remove(probePath)); err != nil {
		return err
	}
	saved, err := supervisor.options.Settings.Load(ctx, s.options.Database, request.Selected.Workspace.ID)
	if err != nil {
		return err
	}
	prepared, err := supervisor.options.Prepare(ctx, codex.PrepareOptions{WorkspaceDir: root, DatabasePath: s.options.Database.Path, ClientVersion: supervisor.options.ClientVersion, Saved: saved.Settings, Overrides: request.Overrides})
	if err != nil {
		return codexPreflightFailure(err)
	}
	if prepared == nil || prepared.Client == nil {
		return scheduleError("codex_preflight_failed", "Codex preparation did not return a session.")
	}
	defer func() { resultErr = errors.Join(resultErr, prepared.Client.Close()) }()
	if request.ResumeFrom != nil && !request.FreshConversation {
		return supervisor.reconcileConversation(ctx, executionRequest, root, prepared.Client)
	}
	return nil
}
