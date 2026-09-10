package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
)

// ExecutionDriver supplies run policy, consumes Events, and uses the recorded
// transition methods below. It must honor context cancellation and may not
// launch unmanaged processes. Selection, prompts, commit/close policy, and
// scheduling modes remain outside process supervision.
type ExecutionDriver func(context.Context, *WorkspaceExecution) error

type SupervisorOptions struct {
	Recorder         ExecutionRecorder
	Settings         WorkspaceRunSettingsManager
	ClientVersion    string
	Prepare          func(context.Context, codex.PrepareOptions) (*codex.PreparedRun, error)
	InterruptTimeout time.Duration
}

// ExecutionSupervisor is created once per foreground server. Accepted work is
// independent of HTTP contexts; every exit path must call Close before exit.
type ExecutionSupervisor struct {
	options  SupervisorOptions
	mu       sync.Mutex
	stopping bool
	runs     map[*ExecutionHandle]struct{}
	active   map[activeExecutionKey]*WorkspaceExecution
	wait     sync.WaitGroup
	closed   chan struct{}
	once     sync.Once
	err      error
}

// Execution run IDs are local to a project database. Keep the database path
// in the live-process key so two foreground projects may both have run #1.
type activeExecutionKey struct {
	databasePath string
	runID        int64
}

type ExecutionRequest struct {
	Database  Database
	Selected  storage.ResolvedProject
	Capture   storage.RunCapture
	Overrides codex.RunOverrides
}

// ExecutionHandle is a receipt, not ownership transferred to a browser.
// Cancelling Result only stops that caller's wait.
type ExecutionHandle struct {
	stop   chan struct{}
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	run    storage.ExecutionRun
	err    error
}

func (handle *ExecutionHandle) Stop() { handle.once.Do(func() { close(handle.stop); handle.cancel() }) }
func (handle *ExecutionHandle) Result(ctx context.Context) (storage.ExecutionRun, error) {
	select {
	case <-ctx.Done():
		return storage.ExecutionRun{}, ctx.Err()
	case <-handle.done:
		return handle.run, handle.err
	}
}

// ListWorkspaceRuns exposes durable evidence to the local server UI without
// exposing process ownership or allowing browser callers to affect a run.
func (supervisor *ExecutionSupervisor) ListWorkspaceRuns(ctx context.Context, database Database, workspaceID int64, limit int) ([]storage.ExecutionRun, error) {
	return supervisor.options.Recorder.ListWorkspaceRuns(ctx, database, workspaceID, limit)
}

// ReadRun is a read-only durable lookup for an explicitly referenced attempt.
func (supervisor *ExecutionSupervisor) ReadRun(ctx context.Context, database Database, id int64) (storage.ExecutionRun, error) {
	return supervisor.options.Recorder.Read(ctx, database, id)
}

// OwnsRun distinguishes this foreground process from a persisted active row.
// A previous server/custodian may still hold the OS lock; only explicit Resume
// attempts that lock and may reconcile a subsequently settled owner.
func (supervisor *ExecutionSupervisor) OwnsRun(database Database, id int64) bool {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.active[activeExecutionKey{databasePath: database.Path, runID: id}] != nil
}

func NewExecutionSupervisor(ctx context.Context, options SupervisorOptions) *ExecutionSupervisor {
	if options.Prepare == nil {
		options.Prepare = codex.PrepareRun
	}
	if options.InterruptTimeout <= 0 {
		options.InterruptTimeout = 3 * time.Second
	}
	supervisor := &ExecutionSupervisor{options: options, runs: make(map[*ExecutionHandle]struct{}), active: make(map[activeExecutionKey]*WorkspaceExecution), closed: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			supervisor.StopScheduling()
		case <-supervisor.closed:
		}
	}()
	return supervisor
}

// Start admits one explicit run. Canonical Git identity is verified before the
// process-safe lock and again under it. The lock precedes settings, preflight,
// run creation, and every Codex process. start-next alone is not exclusion.
func (supervisor *ExecutionSupervisor) Start(ctx context.Context, request ExecutionRequest, driver ExecutionDriver) (*ExecutionHandle, error) {
	return supervisor.start(ctx, request, driver, nil)
}

// Selection runs under process-safe worktree exclusion, before Codex preflight.
// Empty queues never launch a runtime. The callback must select atomically.
func (supervisor *ExecutionSupervisor) start(ctx context.Context, request ExecutionRequest, driver ExecutionDriver, selectCapture func(context.Context) (*storage.RunCapture, error)) (*ExecutionHandle, error) {
	if driver == nil || supervisor.options.Recorder.Open == nil || supervisor.options.Settings.Open == nil {
		return nil, storage.InvalidExecutionRun("supervisor requires a recorder, settings store, and run driver")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Selected.Project.ID != request.Capture.ProjectID || request.Selected.Workspace.ID != request.Capture.WorkspaceID || request.Selected.Workspace.ProjectID != request.Capture.ProjectID {
		return nil, storage.InvalidExecutionRun("run does not match the resolved workspace")
	}
	identityRun := storage.ExecutionRun{WorkspaceRoot: request.Selected.Workspace.RootPath, WorkspaceGitDir: request.Selected.Workspace.GitDir, GitCommonDir: request.Selected.Project.GitCommonDir}
	root, err := executionRoot(ctx, request.Database, identityRun)
	if err != nil {
		return nil, err
	}
	identity, err := discovery.FindGitIdentity(ctx, root)
	if err != nil {
		return nil, err
	}
	var lock *executionlock.Lock
	if request.Capture.ResumeFrom != nil {
		lock, err = executionlock.AcquireRecovery(identity.GitDir)
	} else {
		lock, err = executionlock.Acquire(identity.GitDir)
	}
	if err != nil {
		return nil, err
	}
	if _, err = executionRoot(ctx, request.Database, identityRun); err != nil {
		lock.Close()
		return nil, err
	}
	if request.Capture.ResumeFrom != nil {
		if _, err := supervisor.validateResume(ctx, request, root, lock); err != nil {
			return nil, errors.Join(err, lock.Close())
		}
	}
	// Copy pointer-bearing request values before asynchronous use.
	encoded, err := json.Marshal(request)
	var snapshot ExecutionRequest
	if err == nil {
		err = json.Unmarshal(encoded, &snapshot)
	}
	if err != nil {
		lock.Close()
		return nil, err
	}
	request = snapshot
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.stopping || ctx.Err() != nil {
		lock.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, domain.NewError(domain.Conflict, "server_stopping", "the foreground server has stopped accepting work", nil)
	}
	if selectCapture != nil {
		capture, err := selectCapture(ctx)
		if err != nil || capture == nil {
			return nil, errors.Join(err, lock.Close())
		}
		request.Capture = *capture
	}
	if err := lock.Record(executionlock.Owner{Database: request.Database.Path}); err != nil {
		lock.Close()
		return nil, err
	}
	workCtx, cancelWork := context.WithCancel(context.Background())
	handle := &ExecutionHandle{stop: make(chan struct{}), done: make(chan struct{}), ctx: workCtx, cancel: cancelWork}
	supervisor.runs[handle] = struct{}{}
	supervisor.wait.Add(1)
	go func() {
		defer supervisor.wait.Done()
		handle.run, handle.err = supervisor.execute(handle, request, root, lock, driver)
		supervisor.mu.Lock()
		delete(supervisor.runs, handle)
		// Retain the first error for bounded exit diagnostics even if a caller
		// discards its receipt. Each receipt/evidence retains its own outcome.
		if supervisor.err == nil {
			supervisor.err = handle.err
		}
		supervisor.mu.Unlock()
		close(handle.done)
	}()
	return handle, nil
}

func (supervisor *ExecutionSupervisor) StopScheduling() {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	supervisor.stopping = true
	for handle := range supervisor.runs {
		handle.Stop()
	}
}

func (supervisor *ExecutionSupervisor) Close() error {
	supervisor.once.Do(func() {
		supervisor.StopScheduling()
		supervisor.wait.Wait()
		close(supervisor.closed)
	})
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	return supervisor.err
}

// WorkspaceExecution exposes the prepared protocol and evidence boundaries,
// without handing process ownership or its lifetime context to the driver.
type WorkspaceExecution struct {
	recorder     ExecutionRecorder
	database     Database
	id           int64
	prepared     *codex.PreparedRun
	ctx          context.Context
	closing      atomic.Bool
	turnStarted  atomic.Bool
	operations   chan struct{}
	events       chan codex.Event
	completion   chan struct{}
	eventDone    chan struct{}
	eventFailure chan error
	actions      chan interactionAction
}

func (execution *WorkspaceExecution) ThreadStartParams() map[string]any {
	return execution.prepared.ThreadStartParams()
}
func (execution *WorkspaceExecution) PromptPrefix() storage.PromptPrefix {
	return execution.prepared.PromptPrefix
}
func (execution *WorkspaceExecution) TurnStartParams(thread string, input []any) (map[string]any, error) {
	return execution.prepared.TurnStartParams(thread, input)
}
func (execution *WorkspaceExecution) Events() <-chan codex.Event { return execution.events }
func (execution *WorkspaceExecution) LatestCompletion() *codex.Event {
	return execution.prepared.Client.LatestCompletion()
}
func (execution *WorkspaceExecution) Read(ctx context.Context) (storage.ExecutionRun, error) {
	return execution.recorder.Read(ctx, execution.database, execution.id)
}

func (execution *WorkspaceExecution) operationContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if execution.closing.Load() {
		return nil, nil, codex.ErrClosed
	}
	if err := execution.ctx.Err(); err != nil {
		return nil, nil, err
	}
	combined, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(execution.ctx, cancel)
	return combined, func() { stop(); cancel() }, nil
}

func (execution *WorkspaceExecution) Call(ctx context.Context, operation codex.Operation, params map[string]any) (json.RawMessage, error) {
	ctx, cancel, err := execution.operationContext(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	select {
	case execution.operations <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-execution.operations }()
	if execution.closing.Load() {
		return nil, codex.ErrClosed
	}
	switch operation {
	case codex.ThreadStart, codex.ThreadResume, codex.TurnStart, codex.TurnInterrupt, codex.ReviewStart:
		run, err := execution.Read(ctx)
		if err != nil {
			return nil, err
		}
		_, response, err := execution.recorder.CallCodex(ctx, execution.database, run.ID, run.Revision, execution.prepared.Client, operation, params)
		if (operation == codex.TurnStart || operation == codex.ReviewStart) && err == nil {
			execution.turnStarted.Store(true)
		}
		return response, err
	default:
		return execution.prepared.Client.Call(ctx, operation, params)
	}
}

func (execution *WorkspaceExecution) Respond(ctx context.Context, id json.RawMessage, result any, rpcErr *codex.RPCError) error {
	ctx, cancel, err := execution.operationContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	return execution.prepared.Client.Respond(ctx, id, result, rpcErr)
}

func (execution *WorkspaceExecution) Save(ctx context.Context, progress storage.RunProgress, revision int64) (storage.ExecutionRun, error) {
	ctx, cancel, err := execution.operationContext(ctx)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	defer cancel()
	if (progress.State == "completed" || progress.Finalization != nil) && completedTurnStatus(execution.LatestCompletion(), progress.ThreadID, progress.TurnID) != "completed" {
		current, err := execution.Read(ctx)
		if err != nil {
			return storage.ExecutionRun{}, err
		}
		// The immutable finalization record is written only after the exact
		// successful implementation terminal event. Explicit recovery can use
		// that durable proof without repeating a model turn or its tests.
		if current.ResumeFrom == nil || current.Finalization == nil || progress.ThreadID != current.ThreadID || progress.TurnID != current.TurnID {
			return storage.ExecutionRun{}, storage.InvalidExecutionRun("completion requires the exact turn's successful terminal notification or durable finalization proof")
		}
	}
	return execution.recorder.Save(ctx, execution.database, storage.UpdateExecutionRun{ID: execution.id, ExpectedRevision: revision, Progress: progress})
}

func (execution *WorkspaceExecution) CompleteReviewCheckpoint(ctx context.Context, revision int64) (storage.ExecutionRun, error) {
	ctx, cancel, err := execution.operationContext(ctx)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	defer cancel()
	run, err := execution.Read(ctx)
	if err != nil {
		return run, err
	}
	if completedTurnStatus(execution.LatestCompletion(), run.ThreadID, run.TurnID) != "completed" && run.CheckpointTriage == nil && (run.ResumeFrom == nil || run.ReviewResult == nil) {
		return run, storage.InvalidExecutionRun("review completion requires the exact review turn's successful terminal notification")
	}
	return execution.recorder.CompleteReviewCheckpoint(ctx, execution.database, run.ID, revision)
}

func (execution *WorkspaceExecution) VerifyCommit(ctx context.Context, revision int64, commit string) (storage.ExecutionRun, error) {
	ctx, cancel, err := execution.operationContext(ctx)
	if err != nil {
		return storage.ExecutionRun{}, err
	}
	defer cancel()
	return execution.recorder.VerifyCommit(ctx, execution.database, execution.id, revision, commit)
}

func (execution *WorkspaceExecution) relayEvents() {
	defer close(execution.events)
	defer close(execution.eventDone)
	for event := range execution.prepared.Client.Events() {
		if event.Method == "turn/completed" {
			select {
			case execution.completion <- struct{}{}:
			default:
			}
		}
		if execution.closing.Load() {
			continue
		}
		select {
		case execution.events <- event:
		default:
			select {
			case execution.eventFailure <- codex.ErrBufferFull:
			default:
			}
			execution.closing.Store(true)
		}
	}
}

func (supervisor *ExecutionSupervisor) execute(handle *ExecutionHandle, request ExecutionRequest, root string, lock *executionlock.Lock, driver ExecutionDriver) (run storage.ExecutionRun, resultErr error) {
	defer handle.cancel()
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	processCtx, cancelProcess := context.WithCancel(codex.WithExecutionLock(context.Background(), lock.File()))
	defer cancelProcess()
	// Preflight cancellation may stop its process immediately; an active turn
	// instead receives the explicit bounded interrupt sequence below.
	preflightDone := make(chan struct{})
	preflightWatchDone := make(chan struct{})
	go func() {
		defer close(preflightWatchDone)
		select {
		case <-handle.stop:
			cancelProcess()
		case <-preflightDone:
		}
	}()
	saved, err := supervisor.options.Settings.Load(processCtx, request.Database, request.Selected.Workspace.ID)
	var prepared *codex.PreparedRun
	if err == nil {
		prepared, err = supervisor.options.Prepare(processCtx, codex.PrepareOptions{WorkspaceDir: root, DatabasePath: request.Database.Path, ClientVersion: supervisor.options.ClientVersion, Saved: saved.Settings, Overrides: request.Overrides})
	}
	close(preflightDone)
	<-preflightWatchDone
	if err != nil {
		// Ordinary preflight failures (login, settings, capability) are safe to
		// retry after PrepareRun's cleanup. Ambiguous cleanup retains the fence.
		if !errors.Is(err, codex.ErrCleanup) && lock.Owner() == nil {
			if errors.Is(err, context.Canceled) && processCtx.Err() != nil {
				err = nil
			}
			err = errors.Join(err, lock.Clean())
		}
		return run, err
	}
	if prepared == nil || prepared.Client == nil {
		return run, errors.New("preparation returned no owned Codex session")
	}
	defer func() { resultErr = errors.Join(resultErr, prepared.Client.Close()) }()
	if handle.ctx.Err() != nil {
		if err := prepared.Client.Close(); err != nil {
			return run, err
		}
		if request.Capture.ResumeFrom != nil && lock.Owner() != nil {
			return run, handle.ctx.Err()
		}
		return run, lock.Clean()
	}
	if request.Capture.ResumeFrom != nil {
		if err := supervisor.reconcileConversation(processCtx, request, root, prepared.Client); err != nil {
			// Keep the original attempt and receipt when history is missing or
			// ambiguous. A read-only recovery probe with confirmed cleanup must
			// not manufacture a crash receipt when no old receipt existed.
			closeErr := prepared.Client.Close()
			if closeErr == nil && lock.Owner() == nil {
				closeErr = lock.Clean()
			}
			return run, errors.Join(err, closeErr)
		}
	}
	request.Capture.Settings = prepared.EvidenceSettings
	request.Capture.PromptPrefix = prepared.PromptPrefix
	run, err = supervisor.options.Recorder.Begin(processCtx, request.Database, request.Selected, request.Capture)
	if err != nil {
		return run, err
	}
	if err := lock.Record(executionlock.Owner{Database: request.Database.Path, RunID: run.ID}); err != nil {
		return run, err
	}
	workCtx, cancelWork := codex.WithExecutionLock(handle.ctx, lock.File()), handle.cancel
	execution := &WorkspaceExecution{recorder: supervisor.options.Recorder, database: request.Database, id: run.ID, prepared: prepared, ctx: workCtx,
		operations: make(chan struct{}, 1), events: make(chan codex.Event, prepared.Settings.Limits.EventBuffer), completion: make(chan struct{}, 1), eventDone: make(chan struct{}), eventFailure: make(chan error, 1), actions: make(chan interactionAction)}
	supervisor.mu.Lock()
	key := activeExecutionKey{databasePath: request.Database.Path, runID: run.ID}
	supervisor.active[key] = execution
	supervisor.mu.Unlock()
	defer func() {
		supervisor.mu.Lock()
		delete(supervisor.active, key)
		supervisor.mu.Unlock()
	}()
	go execution.relayEvents()
	driverDone := make(chan error, 1)
	go func() {
		// A policy panic must still stop the owned process and fence evidence.
		defer func() {
			if recover() != nil {
				driverDone <- errors.New("execution driver panicked")
			}
		}()
		driverDone <- driver(workCtx, execution)
	}()
	processDone := make(chan error, 1)
	go func() { processDone <- prepared.Client.Wait(processCtx) }()
	interrupted := false
	driverReturned := false
	failureCode := "execution_driver_stopped"
	select {
	case <-handle.stop:
		interrupted = true
	case err = <-driverDone:
		driverReturned = true
		if err != nil {
			failureCode = domain.PublicError(err).Code
		}
	case err = <-processDone:
		failureCode = "codex_session_stopped"
	case err = <-execution.eventFailure:
		failureCode = "codex_event_overflow"
	}
	select {
	case <-handle.stop:
		interrupted = true
	default:
	}
	execution.closing.Store(true)
	cancelWork()
	interruptCtx, cancelInterrupt := context.WithTimeout(context.Background(), supervisor.options.InterruptTimeout)
	// A driver call cancelled during shutdown gets a chance to reconcile its
	// durable intent before interruption. Uncertain calls are never replayed.
	interruptErr := execution.interrupt(interruptCtx)
	cancelInterrupt()
	closeErr := prepared.Client.Close()
	cancelProcess()
	<-execution.eventDone
	var driverStopErr error
	if !driverReturned {
		select {
		case driverErr := <-driverDone:
			if !errors.Is(driverErr, context.Canceled) || executionFailureDiagnostic(driverErr) != "" || errors.Is(driverErr, codex.ErrCleanup) {
				err = errors.Join(err, driverErr)
			}
		case <-time.After(5 * time.Second):
			driverStopErr = errors.New("execution driver did not return after cancellation; workspace remains fenced")
		}
	}
	finalCtx, cancelFinal := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFinal()
	run, readErr := execution.Read(finalCtx)
	if readErr == nil && run.State != "completed" {
		if interrupted && closeErr == nil && !errors.Is(err, codex.ErrCleanup) {
			if diagnostic := executionFailureDiagnostic(err); diagnostic != "" && storage.RunActive(run.State) {
				progress := run.RunProgress
				progress.Summary = diagnostic
				run, readErr = execution.recorder.Save(finalCtx, request.Database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
				if readErr != nil {
					return run, errors.Join(err, closeErr, readErr, driverStopErr)
				}
			}
			outcome := "unknown"
			switch completedTurnStatus(execution.LatestCompletion(), run.ThreadID, run.TurnID) {
			case "interrupted":
				outcome = "cancelled"
			case "failed":
				outcome = "failed"
			}
			run, readErr = execution.recorder.markInterrupted(finalCtx, request.Database, run.ID, run.Revision, outcome)
		} else if storage.RunActive(run.State) {
			progress := run.RunProgress
			progress.State, progress.Outcome, progress.ErrorCode, progress.Summary = "needs_attention", "unknown", failureCode, ""
			progress.Interaction = nil
			switch failureCode {
			case "codex_input_required":
				progress.Summary = "Codex is awaiting explicit input."
			case "codex_approval_review_required":
				progress.Summary = "Automatic approval review requires attention."
			default:
				progress.Summary = executionFailureDiagnostic(err)
			}
			if closeErr != nil || errors.Is(err, codex.ErrCleanup) {
				progress.ErrorCode = "process_cleanup_unconfirmed"
			}
			run, readErr = execution.recorder.Save(finalCtx, request.Database, storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress})
		}
	}
	// An interrupt timeout is expected escalation, not a cleanup failure. Its
	// stopped/unknown outcome is preserved; Close is the proof of termination.
	if interruptErr != nil && !errors.Is(interruptErr, context.DeadlineExceeded) && !errors.Is(interruptErr, context.Canceled) {
		err = errors.Join(err, interruptErr)
	}
	if closeErr == nil && readErr == nil && driverStopErr == nil && run.PendingOperation == "" && !errors.Is(err, codex.ErrCleanup) {
		readErr = lock.Clean()
	}
	return run, errors.Join(err, closeErr, readErr, driverStopErr)
}

func (execution *WorkspaceExecution) interrupt(ctx context.Context) error {
	select {
	case execution.operations <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-execution.operations }()
	run, err := execution.Read(ctx)
	if err != nil {
		return err
	}
	if run.TurnID == "" || run.ThreadID == "" || run.State == "completed" {
		return nil
	}
	if run.ResumeFrom != nil && !execution.turnStarted.Load() {
		// Recovery carries a historical turn ID. Until this session starts a
		// new turn, neither finalization nor checkpoint reconciliation owns
		// an active generation that it may interrupt.
		return nil
	}
	if completedTurn(execution.LatestCompletion(), run.ThreadID, run.TurnID) {
		return nil
	}
	if !storage.RunActive(run.State) || run.PendingOperation != "" {
		return nil
	}
	_, _, err = execution.recorder.CallCodex(ctx, execution.database, run.ID, run.Revision, execution.prepared.Client, codex.TurnInterrupt, map[string]any{"threadId": run.ThreadID, "turnId": run.TurnID})
	if err != nil {
		return err
	}
	for {
		if completedTurn(execution.LatestCompletion(), run.ThreadID, run.TurnID) {
			return nil
		}
		select {
		case <-execution.completion:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func completedTurn(event *codex.Event, thread, turn string) bool {
	status := completedTurnStatus(event, thread, turn)
	return status == "completed" || status == "interrupted" || status == "failed"
}

func completedTurnStatus(event *codex.Event, thread, turn string) string {
	if event == nil || event.Method != "turn/completed" {
		return ""
	}
	var params struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(event.Params, &params) != nil {
		return ""
	}
	if params.ThreadID == thread && params.Turn.ID == turn {
		return params.Turn.Status
	}
	return ""
}
