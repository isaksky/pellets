package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

const DefaultScheduleLimit = 100

type ScheduleRequest struct {
	Selected     storage.ResolvedProject `json:"-"`
	Mode         string                  `json:"mode"`
	ExternalID   *string                 `json:"external_id"`
	Group        *string                 `json:"group"`
	ResumePellet *int64                  `json:"resume_pellet,omitempty"`
	ResumeFrom   *int64                  `json:"resume_from,omitempty"`
	Limit        int                     `json:"limit"`
	Overrides    codex.RunOverrides      `json:"overrides"`
}

type ScheduleStatus struct {
	ID              int64   `json:"id"`
	WorkspaceID     int64   `json:"workspace_id"`
	ProjectID       int64   `json:"project_id"`
	Mode            string  `json:"mode"`
	ExternalID      *string `json:"external_id"`
	Group           *string `json:"group"`
	Limit           int     `json:"limit"`
	Started         int     `json:"started"`
	Completed       int     `json:"completed"`
	PelletNumber    int64   `json:"pellet_number,omitempty"`
	RunID           int64   `json:"run_id,omitempty"`
	State           string  `json:"state"`
	Reason          string  `json:"reason,omitempty"`
	Detail          string  `json:"detail,omitempty"`
	StopAfterPellet bool    `json:"stop_after_pellet"`
}

type SchedulerOptions struct {
	Database   Database
	Supervisor *ExecutionSupervisor
	OpenQueue  func(context.Context, string) (storage.SchedulerQueue, error)
	// Subscribe must be installed before the first selection to avoid missed
	// wakeups. The server shares its data-version monitor with browser streams.
	Subscribe        func() (<-chan struct{}, func())
	RecoveryInterval time.Duration
	// Ready is a narrow, read-only checkpoint policy extension; see storage's
	// transaction contract. Nil means ordinary queue semantics.
	Ready func(context.Context, storage.ResolvedProject, storage.Pellet) (bool, error)
	// Checkpoints have their own driver and completion evidence. Ordinary
	// clean-worktree and single implementation commit rules do not apply.
	Checkpoints *CheckpointExecutionPolicy
}

type CheckpointExecutionPolicy struct {
	Matches            func(storage.Pellet) bool
	Drive              ExecutionDriver
	ValidateCompletion func(context.Context, storage.ExecutionRun, storage.ResolvedProject) error
	// Resume must reconcile the saved phase and any durable triage receipts
	// idempotently. It is never substituted with Drive after an interruption.
	Resume ExecutionDriver
}

// Scheduler has foreground lifetime only. Restart never recreates schedules;
// durable attempts remain in the recorder for explicit reconciliation.
type Scheduler struct {
	options    SchedulerOptions
	mu         sync.Mutex
	stopping   bool
	nextID     int64
	schedules  map[int64]*ScheduleHandle
	workspaces map[int64]*ScheduleHandle
	wait       sync.WaitGroup
	closed     chan struct{}
	once       sync.Once
}

type ScheduleHandle struct {
	mu        sync.Mutex
	status    ScheduleStatus
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	done      chan struct{}
	execution *ExecutionHandle
}

func NewScheduler(ctx context.Context, options SchedulerOptions) *Scheduler {
	if options.RecoveryInterval <= 0 {
		options.RecoveryInterval = 30 * time.Second
	}
	s := &Scheduler{options: options, schedules: make(map[int64]*ScheduleHandle), workspaces: make(map[int64]*ScheduleHandle), closed: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			s.StopScheduling()
		case <-s.closed:
		}
	}()
	return s
}

func (s *Scheduler) Start(ctx context.Context, request ScheduleRequest) (*ScheduleHandle, error) {
	if s.options.Supervisor == nil || s.options.OpenQueue == nil {
		return nil, scheduleError("scheduler_unavailable", "foreground scheduler is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Mode != "run_one" && request.Mode != "drain" && request.Mode != "watch" {
		return nil, storage.InvalidExecutionRun("choose run_one, drain, or watch")
	}
	if request.Selected.Workspace.ID < 1 || request.Selected.Project.ID != request.Selected.Workspace.ProjectID {
		return nil, storage.InvalidExecutionRun("choose an existing registered workspace explicitly")
	}
	if request.Limit == 0 {
		request.Limit = DefaultScheduleLimit
	}
	if request.Limit < 1 || request.Limit > 10000 {
		return nil, storage.InvalidExecutionRun("schedule limit must be between 1 and 10000")
	}
	if request.ResumePellet != nil && *request.ResumePellet < 1 || request.ResumeFrom != nil && (request.ResumePellet == nil || *request.ResumeFrom < 1) {
		return nil, storage.InvalidExecutionRun("Resume requires the exact positive pellet and optional attempt ID")
	}
	if request.ResumeFrom != nil {
		previous, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, *request.ResumeFrom)
		if err != nil {
			return nil, err
		}
		if previous.ProjectID != request.Selected.Project.ID || previous.WorkspaceID != request.Selected.Workspace.ID || previous.PelletNumber != *request.ResumePellet {
			return nil, storage.ExecutionRunConflict(previous.ID)
		}
		request.Mode, request.Limit = previous.ScheduleMode, previous.ScheduleRemaining
		request.ExternalID, request.Group = copyScheduleFilter(previous.ExternalID), copyScheduleFilter(previous.Group)
	}
	for _, value := range []*string{request.ExternalID, request.Group} {
		if value != nil && (*value == "" || len(*value) > 4096 || !utf8.ValidString(*value)) {
			return nil, storage.InvalidExecutionRun("filters must be nonempty exact UTF-8 values")
		}
	}
	// Freeze every pointer and slice, including overrides and selected paths.
	selected, err := json.Marshal(request.Selected)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var frozen ScheduleRequest
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(selected, &frozen.Selected); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return nil, scheduleError("server_stopping", "the foreground server has stopped accepting schedules")
	}
	if s.workspaces[frozen.Selected.Workspace.ID] != nil {
		return nil, scheduleError("workspace_schedule_busy", "this workspace already has an active schedule")
	}
	s.nextID++
	workCtx, cancel := context.WithCancel(context.Background())
	h := &ScheduleHandle{ctx: workCtx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), status: ScheduleStatus{ID: s.nextID, WorkspaceID: frozen.Selected.Workspace.ID, ProjectID: frozen.Selected.Project.ID, Mode: frozen.Mode, ExternalID: frozen.ExternalID, Group: frozen.Group, Limit: frozen.Limit, State: "selecting"}}
	s.schedules[h.status.ID], s.workspaces[h.status.WorkspaceID] = h, h
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		defer cancel()
		s.run(h, frozen)
		s.mu.Lock()
		delete(s.workspaces, frozen.Selected.Workspace.ID)
		s.mu.Unlock()
		close(h.done)
	}()
	return h, nil
}

func (s *Scheduler) Get(id int64) (*ScheduleHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.schedules[id]
	if h == nil {
		return nil, domain.NewError(domain.NotFound, "schedule_not_found", "the exact foreground schedule is unavailable", nil)
	}
	return h, nil
}

// WorkspaceStatus returns the current foreground receipt for the exact
// workspace. Schedules are process-local; callers must combine this with the
// durable run record for reconnect-safe rendering.
func (s *Scheduler) WorkspaceStatus(workspaceID int64) (ScheduleStatus, bool) {
	s.mu.Lock()
	h := s.workspaces[workspaceID]
	s.mu.Unlock()
	if h == nil {
		return ScheduleStatus{}, false
	}
	return h.Status(), true
}

// WorkspaceAttention retains a failed admission/recovery explanation even
// when no new durable attempt could safely be created.
func (s *Scheduler) WorkspaceAttention(workspaceID int64) string {
	s.mu.Lock()
	var latest *ScheduleHandle
	var id int64
	for candidate, handle := range s.schedules {
		if candidate > id && handle.status.WorkspaceID == workspaceID {
			latest, id = handle, candidate
		}
	}
	s.mu.Unlock()
	if latest == nil {
		return ""
	}
	status := latest.Status()
	if status.State == "needs_attention" {
		return status.Detail
	}
	return ""
}

func (h *ScheduleHandle) Status() ScheduleStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := h.status
	if status.ExternalID != nil {
		value := *status.ExternalID
		status.ExternalID = &value
	}
	if status.Group != nil {
		value := *status.Group
		status.Group = &value
	}
	return status
}

func (h *ScheduleHandle) Result(ctx context.Context) (ScheduleStatus, error) {
	select {
	case <-ctx.Done():
		return ScheduleStatus{}, ctx.Err()
	case <-h.done:
		return h.Status(), nil
	}
}

// StopAfter allows the active pellet to reach validated completion. While
// idle it stops immediately. StopNow cancels admission and interrupts Codex.
func (h *ScheduleHandle) StopAfter() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.StopAfterPellet = true
	select {
	case h.wake <- struct{}{}:
	default:
	}
}
func (h *ScheduleHandle) StopNow() {
	h.cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.execution != nil {
		h.execution.Stop()
	}
}
func (s *Scheduler) StopScheduling() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
	for _, h := range s.workspaces {
		h.StopNow()
	}
}
func (s *Scheduler) Close() error {
	s.once.Do(func() { s.StopScheduling(); s.wait.Wait(); close(s.closed) })
	return nil
}

func (h *ScheduleHandle) finish(state, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.status.State, h.status.Reason = state, reason
	h.execution = nil
}

func (h *ScheduleHandle) attention(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	public := domain.PublicError(err)
	h.status.State, h.status.Reason, h.status.Detail = "needs_attention", public.Code, public.Message
	h.execution = nil
}

func (s *Scheduler) run(h *ScheduleHandle, request ScheduleRequest) {
	var changes <-chan struct{}
	if s.options.Subscribe != nil {
		var unsubscribe func()
		changes, unsubscribe = s.options.Subscribe()
		defer unsubscribe()
	}
	for {
		h.mu.Lock()
		if h.ctx.Err() != nil || h.status.StopAfterPellet {
			h.mu.Unlock()
			h.finish("stopped", "stop_requested")
			return
		}
		h.status.State, h.status.Reason = "selecting", ""
		reason := storage.NextNone
		execution, err := s.options.Supervisor.start(h.ctx, ExecutionRequest{Database: s.options.Database, Selected: request.Selected, Capture: storage.RunCapture{ProjectID: request.Selected.Project.ID, WorkspaceID: request.Selected.Workspace.ID, ResumeFrom: request.ResumeFrom}, Overrides: request.Overrides}, s.drive, func(ctx context.Context) (*storage.RunCapture, error) {
			queue, err := s.options.OpenQueue(ctx, s.options.Database.Path)
			if err != nil {
				return nil, err
			}
			if request.ResumePellet != nil && request.ResumeFrom == nil {
				runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(ctx, s.options.Database, request.Selected.Workspace.ID, 1)
				if err != nil {
					queue.Close()
					return nil, err
				}
				pellet, err := queue.ReadPellet(ctx, request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: *request.ResumePellet})
				if err != nil {
					queue.Close()
					return nil, err
				}
				if len(runs) != 0 && storage.RunMatchesPelletGeneration(runs[0], pellet) {
					queue.Close()
					return nil, scheduleError("schedule_exact_resume_required", "Resume must reference the exact saved attempt and conversation")
				}
			}
			// Explicit reconciliation may observe that close committed before
			// the terminal run save. It does not reopen or select another pellet.
			if request.ResumeFrom != nil {
				previous, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, *request.ResumeFrom)
				if err != nil {
					queue.Close()
					return nil, err
				}
				if previous.ProjectID != request.Selected.Project.ID || previous.WorkspaceID != request.Selected.Workspace.ID || previous.PelletNumber != *request.ResumePellet {
					queue.Close()
					return nil, storage.ExecutionRunConflict(previous.ID)
				}
				if previous.Mode == "review_checkpoint" && (s.options.Checkpoints == nil || s.options.Checkpoints.Resume == nil) {
					queue.Close()
					return nil, scheduleError("checkpoint_resume_policy_required", "this checkpoint needs its idempotent recovery policy before Resume; its saved phase and follow-ups are preserved")
				}
				p, err := queue.ReadPellet(ctx, request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: previous.PelletNumber})
				// Open/deferred pellets still fail the atomic ownership/lifecycle
				// check below; preserve that more specific recovery explanation.
				generationChanged := (p.Status == domain.PelletInProgress || p.Status == domain.PelletClosed) && !storage.RunMatchesPelletGeneration(previous, p)
				if err != nil || generationChanged || p.Title != previous.PelletTitle || p.Description != previous.PelletDescription || (previous.Mode == "review_checkpoint") != s.isCheckpoint(p) {
					queue.Close()
					return nil, errors.Join(scheduleError("resume_scope_changed", "the saved pellet scope, filters, or checkpoint policy changed; reconcile that edit before Resume"), err)
				}
				reviewReceipt, lineageErr := s.completedReviewLineage(ctx, previous)
				if lineageErr != nil {
					queue.Close()
					return nil, lineageErr
				}
				if (previous.Finalization != nil && previous.ResultCommit != "" && (previous.Phase == "close" || previous.State == "completed")) || reviewReceipt {
					p, err := queue.ReadPellet(ctx, request.Selected, domain.PelletReference{ProjectCode: request.Selected.Project.Code, Number: previous.PelletNumber})
					if err != nil {
						queue.Close()
						return nil, err
					}
					if p.Status == domain.PelletClosed && storage.MatchesSchedule(p, request.ExternalID, request.Group) {
						if err := queue.Close(); err != nil {
							return nil, err
						}
						h.status.PelletNumber = previous.PelletNumber
						return &storage.RunCapture{ProjectID: previous.ProjectID, WorkspaceID: previous.WorkspaceID, PelletNumber: previous.PelletNumber, Mode: previous.Mode, ScheduleMode: request.Mode, ScheduleRemaining: request.Limit - h.status.Completed, ExternalID: request.ExternalID, Group: request.Group, ResumeFrom: request.ResumeFrom}, nil
					}
				}
			}
			selection := storage.ScheduleSelection{ExternalID: request.ExternalID, Group: request.Group, ResumePellet: request.ResumePellet}
			selection.Ready = func(ctx context.Context, p storage.Pellet) (bool, error) {
				if p.Kind == domain.PelletReviewCheckpoint && request.ResumeFrom == nil && (s.options.Checkpoints == nil || s.options.Checkpoints.Drive == nil) {
					return false, scheduleError("checkpoint_policy_required", "checkpoint execution requires the separate review policy")
				}
				if request.ResumePellet == nil && !s.isCheckpoint(p) {
					root, err := executionRoot(ctx, s.options.Database, storage.ExecutionRun{WorkspaceRoot: request.Selected.Workspace.RootPath, WorkspaceGitDir: request.Selected.Workspace.GitDir, GitCommonDir: request.Selected.Project.GitCommonDir})
					if err != nil {
						return false, err
					}
					if err := requireCleanWorktree(ctx, root); err != nil {
						return false, err
					}
				}
				if s.options.Ready != nil {
					return s.options.Ready(ctx, request.Selected, p)
				}
				return true, nil
			}
			selected, err := queue.SelectScheduledPellet(ctx, request.Selected, selection)
			err = errors.Join(err, queue.Close())
			if err != nil {
				return nil, err
			}
			reason = selected.Reason
			if selected.Pellet == nil {
				return nil, nil
			}
			h.status.PelletNumber = selected.Pellet.Reference.Number
			mode := request.Mode
			if s.isCheckpoint(*selected.Pellet) {
				mode = "review_checkpoint"
			}
			return &storage.RunCapture{ProjectID: request.Selected.Project.ID, WorkspaceID: request.Selected.Workspace.ID, PelletNumber: selected.Pellet.Reference.Number, Mode: mode, ScheduleMode: request.Mode, ScheduleRemaining: request.Limit - h.status.Completed, ExternalID: request.ExternalID, Group: request.Group, ResumeFrom: request.ResumeFrom}, nil
		})
		h.execution = execution
		if execution != nil {
			h.status.Started++
			h.status.State = "running"
		}
		h.mu.Unlock()
		if err != nil {
			if h.ctx.Err() != nil {
				h.finish("stopped", "stop_requested")
			} else {
				h.attention(err)
			}
			return
		}
		if execution == nil {
			if request.Mode != "watch" {
				if reason == storage.NextNotReady {
					h.finish("stopped", "checkpoint_not_ready")
				} else {
					h.finish("completed", "queue_empty")
				}
				return
			}
			h.finish("waiting", string(reason))
			recovery := time.NewTimer(s.options.RecoveryInterval)
			select {
			case <-h.ctx.Done():
			case <-h.wake:
			case _, open := <-changes:
				if !open {
					changes = nil
				}
			case <-recovery.C:
			}
			recovery.Stop()
			continue
		}
		// Waiting for the receipt includes supervisor process cleanup, evidence
		// persistence, and lock release. A browser context cannot end this wait.
		run, err := execution.Result(context.Background())
		h.mu.Lock()
		h.execution = nil
		h.status.RunID = run.ID
		expectedPellet := h.status.PelletNumber
		h.mu.Unlock()
		if h.ctx.Err() != nil {
			h.finish("stopped", "stop_requested")
			return
		}
		if err != nil {
			h.attention(err)
			return
		}
		if run.PelletNumber != expectedPellet {
			h.finish("needs_attention", "schedule_target_changed")
			return
		}
		if err := s.validateCompletion(h.ctx, run, request.Selected); err != nil {
			h.attention(err)
			return
		}
		h.mu.Lock()
		h.status.Completed++
		completed, stop := h.status.Completed, h.status.StopAfterPellet
		h.mu.Unlock()
		if stop {
			h.finish("stopped", "stop_after_pellet")
			return
		}
		if request.Mode == "run_one" {
			h.finish("completed", "run_one_complete")
			return
		}
		if completed >= request.Limit {
			h.finish("stopped", "limit_reached")
			return
		}
		request.ResumePellet, request.ResumeFrom = nil, nil
	}
}

func scheduleError(code, message string) error {
	return domain.NewError(domain.Conflict, code, message, nil)
}

func (s *Scheduler) completedReviewLineage(ctx context.Context, run storage.ExecutionRun) (bool, error) {
	seen := make(map[int64]bool)
	for depth := 0; depth < 1000; depth++ {
		if seen[run.ID] {
			return false, nil
		}
		seen[run.ID] = true
		if storage.CompletedReviewReceipt(run) {
			return true, nil
		}
		if !storage.ReviewReconciliationAttempt(run) {
			return false, nil
		}
		parent, err := s.options.Supervisor.options.Recorder.Read(ctx, s.options.Database, *run.ResumeFrom)
		if err != nil {
			return false, err
		}
		if !storage.SameReviewReconciliationEvidence(run, parent) {
			return false, nil
		}
		run = parent
	}
	return false, nil
}

func (s *Scheduler) validateCompletion(ctx context.Context, run storage.ExecutionRun, selected storage.ResolvedProject) error {
	if run.Mode == "review_checkpoint" {
		if s.options.Checkpoints == nil || s.options.Checkpoints.ValidateCompletion == nil {
			return scheduleError("checkpoint_policy_required", "checkpoint completion policy is required")
		}
		return s.options.Checkpoints.ValidateCompletion(ctx, run, selected)
	}
	if run.State != "completed" || run.Outcome != "succeeded" || run.PendingOperation != "" || run.ProjectID != selected.Project.ID || run.WorkspaceID != selected.Workspace.ID || run.ThreadID == "" || run.TurnID == "" || run.CommitVerifiedAt == nil || run.ResultCommit == "" || run.ResultCommit == run.StartingHead || run.Finalization == nil {
		return scheduleError("schedule_completion_unverified", "the exact attempt lacks validated completion evidence")
	}
	return s.validateResult(ctx, run, selected)
}

func (s *Scheduler) validateResult(ctx context.Context, run storage.ExecutionRun, selected storage.ResolvedProject) error {
	queue, err := s.options.OpenQueue(ctx, s.options.Database.Path)
	if err != nil {
		return err
	}
	pellet, err := queue.ReadPellet(ctx, selected, domain.PelletReference{ProjectCode: selected.Project.Code, Number: run.PelletNumber})
	err = errors.Join(err, queue.Close())
	if err != nil {
		return err
	}
	if pellet.Status != domain.PelletClosed {
		return scheduleError("schedule_pellet_not_closed", "the exact pellet has not been closed")
	}
	root, err := executionRoot(ctx, s.options.Database, run)
	if err != nil {
		return err
	}
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != run.ResultCommit || head == run.StartingHead {
		return errors.Join(missingRunEvidence("result_commit_not_head"), err)
	}
	if err := verifyRunCommit(ctx, root, head); err != nil {
		return err
	}
	if run.Finalization != nil {
		if err := validateFinalizationCommit(ctx, root, run, head); err != nil {
			return err
		}
		if err := requireCleanWorktree(ctx, root); err != nil {
			return err
		}
	}
	_, err = executionGit(ctx, root, "merge-base", "--is-ancestor", run.StartingHead, head)
	return err
}

func (s *Scheduler) isCheckpoint(p storage.Pellet) bool {
	return p.Kind == domain.PelletReviewCheckpoint || (s.options.Checkpoints != nil && s.options.Checkpoints.Matches != nil && s.options.Checkpoints.Matches(p))
}
