package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
	"pellets/internal/testcodex"
)

func TestMain(m *testing.M) {
	if testcodex.Run() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func installSupervisorPeer(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := "pl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	if err := os.Link(executable, target); err != nil {
		input, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(output, input)
		if err := errors.Join(err, input.Close(), output.Close()); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PELLETS_SUPERVISOR_PEER", "1")
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return executable
}

func supervisorFixture(t *testing.T, executable string) (*ExecutionSupervisor, ExecutionRequest) {
	t.Helper()
	recorder, database, selected, capture := executionFixture(t)
	settings := WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
		return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
	}}
	_, err := settings.Save(context.Background(), database, storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: selected.Workspace.ID, Settings: storage.CodexRunSettings{Executable: executable, Model: "test-model", ReasoningEffort: "high"}})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := NewExecutionSupervisor(context.Background(), SupervisorOptions{Recorder: recorder, Settings: settings, InterruptTimeout: 300 * time.Millisecond})
	t.Cleanup(func() { supervisor.Close() })
	return supervisor, ExecutionRequest{Database: database, Selected: selected, Capture: capture}
}

func holdingDriver(ready chan<- *WorkspaceExecution) ExecutionDriver {
	return func(ctx context.Context, execution *WorkspaceExecution) error {
		if _, err := execution.Call(ctx, codex.ThreadStart, execution.ThreadStartParams()); err != nil {
			return err
		}
		params, err := execution.TurnStartParams("thread", []any{map[string]any{"type": "text", "text": "deterministic test"}})
		if err != nil {
			return err
		}
		if _, err := execution.Call(ctx, codex.TurnStart, params); err != nil {
			return err
		}
		ready <- execution
		for {
			select {
			case <-ctx.Done():
				return nil
			case _, ok := <-execution.Events():
				if !ok {
					return nil
				}
			}
		}
	}
}

func awaitExecution(t *testing.T, ready <-chan *WorkspaceExecution, handle *ExecutionHandle) *WorkspaceExecution {
	t.Helper()
	select {
	case execution := <-ready:
		return execution
	case <-handle.done:
		t.Fatalf("execution failed before ready: %v", handle.err)
	case <-time.After(15 * time.Second):
		t.Fatal("execution did not become ready")
	}
	return nil
}

type peerEvent struct {
	PID         int `json:"pid"`
	CWD, Method string
	Params      json.RawMessage
}

func readPeerEvents(t *testing.T, root string) []peerEvent {
	t.Helper()
	file, err := os.Open(filepath.Join(root, "fake-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var events []peerEvent
	for scanner.Scan() {
		var event peerEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func requireStoppedPeers(t *testing.T, events []peerEvent) {
	t.Helper()
	pids := map[int]bool{}
	for _, event := range events {
		if event.Method == "process" {
			pids[event.PID] = true
		}
	}
	if len(pids) != 3 {
		t.Fatalf("expected root, child, grandchild; got %v", pids)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pids) > 0 {
		for pid := range pids {
			if !testcodex.Alive(pid) {
				delete(pids, pid)
			}
		}
		if len(pids) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned processes remain: %v", pids)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSupervisorDisconnectShutdownAndDescendants(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"graceful", "forced", "hang-interrupt"} {
		t.Run(mode, func(t *testing.T) {
			supervisor, request := supervisorFixture(t, executable)
			if err := os.WriteFile(filepath.Join(request.Database.Root, "fake-mode"), []byte(mode), 0600); err != nil {
				t.Fatal(err)
			}
			ready := make(chan *WorkspaceExecution, 1)
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			handle, err := supervisor.Start(requestCtx, request, holdingDriver(ready))
			if err != nil {
				t.Fatal(err)
			}
			execution := awaitExecution(t, ready, handle)
			cancelRequest()
			if _, err := handle.Result(requestCtx); !errors.Is(err, context.Canceled) {
				t.Fatalf("request wait = %v", err)
			}
			if _, err := execution.Call(context.Background(), codex.ThreadRead, map[string]any{"threadId": "thread"}); err != nil {
				t.Fatalf("browser disconnected active work: %v", err)
			}
			if _, err := supervisor.Start(context.Background(), request, holdingDriver(ready)); domain.PublicError(err).Code != "workspace_execution_busy" {
				t.Fatalf("same workspace start: %v", err)
			}
			started := time.Now()
			if err := supervisor.Close(); err != nil && mode != "hang-interrupt" {
				t.Fatal(err)
			}
			if time.Since(started) > 8*time.Second {
				t.Fatal("shutdown exceeded its bound")
			}
			run, err := handle.Result(context.Background())
			if err != nil && mode != "hang-interrupt" {
				t.Fatal(err)
			}
			outcome := "unknown"
			if mode == "graceful" {
				outcome = "cancelled"
			}
			if run.State != "interrupted" || run.Outcome != outcome || run.ThreadID != "thread" || run.TurnID != "turn" || run.PendingOperation != "" {
				t.Fatalf("shutdown evidence: %#v", run)
			}
			events := readPeerEvents(t, request.Database.Root)
			interrupts := 0
			for _, event := range events {
				if event.Method == "turn/interrupt" {
					interrupts++
				}
			}
			if interrupts != 1 {
				t.Fatalf("interrupt requests = %d", interrupts)
			}
			requireStoppedPeers(t, events)
			identity, err := discovery.FindGitIdentity(context.Background(), request.Database.Root)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := executionlock.Acquire(identity.GitDir)
			if err != nil {
				t.Fatalf("lock was not released after cleanup: %v", err)
			}
			lock.Close()
			if _, err := supervisor.Start(context.Background(), request, holdingDriver(ready)); domain.PublicError(err).Code != "server_stopping" {
				t.Fatalf("post-shutdown admission: %v", err)
			}
		})
	}
}

func TestSupervisorIndependentProjectsAndPreflightFailures(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, first := supervisorFixture(t, executable)
	_, second := supervisorFixture(t, executable)
	ready := make(chan *WorkspaceExecution, 2)
	firstHandle, err := supervisor.Start(context.Background(), first, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, firstHandle)
	if err := os.WriteFile(filepath.Join(second.Database.Root, "fake-mode"), []byte("unauthenticated"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := supervisor.Start(context.Background(), second, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failed.Result(context.Background()); !errors.Is(err, codex.ErrUnauthenticated) {
		t.Fatalf("preflight: %v", err)
	}
	if err := os.WriteFile(filepath.Join(second.Database.Root, "fake-mode"), []byte("graceful"), 0600); err != nil {
		t.Fatal(err)
	}
	secondHandle, err := supervisor.Start(context.Background(), second, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, secondHandle)
	firstHandle.Stop()
	if _, err := firstHandle.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondHandle.done:
		t.Fatal("one project's stop affected another")
	default:
	}
	secondHandle.Stop()
	if _, err := secondHandle.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, request := range []ExecutionRequest{first, second} {
		events := readPeerEvents(t, request.Database.Root)
		root, _ := filepath.EvalSymlinks(request.Database.Root)
		for _, event := range events {
			if event.CWD != root {
				t.Fatalf("crossed cwd: %#v", event)
			}
		}
	}
}

func TestSupervisorRuntimeCrashIsNotSuccess(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	if err := os.WriteFile(filepath.Join(request.Database.Root, "fake-mode"), []byte("crash"), 0600); err != nil {
		t.Fatal(err)
	}
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	execution := awaitExecution(t, ready, handle)
	_, _ = execution.Call(context.Background(), codex.ThreadRead, map[string]any{"threadId": "thread"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run, err := handle.Result(ctx)
	if errors.Is(err, context.DeadlineExceeded) || run.State != "needs_attention" || run.Outcome != "unknown" {
		t.Fatalf("runtime crash outcome %#v %v", run, err)
	}
	requireStoppedPeers(t, readPeerEvents(t, request.Database.Root))
}

func TestSupervisorLockHelper(t *testing.T) {
	root := os.Getenv("PELLETS_LOCK_HELPER_ROOT")
	if root == "" {
		return
	}
	lock, err := executionlock.Acquire(filepath.Join(root, ".git"))
	if domain.PublicError(err).Code != "workspace_execution_busy" {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("competing process: %v", err)
	}
}

func TestSupervisorExcludesSeparateProcessAndAlias(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, handle)
	cmd := exec.Command(executable, "-test.run=^TestSupervisorLockHelper$")
	cmd.Env = append(os.Environ(), "PELLETS_LOCK_HELPER_ROOT="+request.Database.Root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process exclusion: %v %s", err, output)
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(request.Database.Root, alias); err != nil {
			t.Fatal(err)
		}
		identity, err := discovery.FindGitIdentity(context.Background(), alias)
		if err != nil {
			t.Fatal(err)
		}
		if lock, err := executionlock.Acquire(identity.GitDir); domain.PublicError(err).Code != "workspace_execution_busy" {
			if lock != nil {
				lock.Close()
			}
			t.Fatalf("alias bypassed exclusion: %v", err)
		}
	}
	handle.Stop()
	if _, err := handle.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorChangedWorkspaceIdentityDoesNotLaunch(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	request.Selected.Workspace.GitDir.Value += "-wrong"
	_, err := supervisor.Start(context.Background(), request, holdingDriver(make(chan *WorkspaceExecution, 1)))
	if !strings.Contains(domain.PublicError(err).Code, "identity_changed") {
		t.Fatalf("invalid identity = %v", err)
	}
	if _, err := os.Stat(filepath.Join(request.Database.Root, "fake-events.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid workspace launched Codex: %v", err)
	}
}

func TestSupervisorLinkedWorktreesExecuteConcurrently(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, first := supervisorFixture(t, executable)
	secondRoot := filepath.Join(t.TempDir(), "linked")
	gitForExecutionTest(t, first.Database.Root, "worktree", "add", "-b", "test-linked", secondRoot)
	identity, err := discovery.FindGitIdentity(context.Background(), secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	rootPath, err := discovery.NormalizeLocalPath(first.Database.Root, identity.WorkTreeRoot)
	if err != nil {
		t.Fatal(err)
	}
	gitDir, err := discovery.NormalizeLocalPath(first.Database.Root, identity.GitDir)
	if err != nil {
		t.Fatal(err)
	}
	projects, err := sqlite.OpenProjectDatabase(context.Background(), first.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = projects.RegisterProject(context.Background(), storage.ProjectRegistration{Code: first.Selected.Project.Code, GitCommonDir: first.Selected.Project.GitCommonDir, GitDir: gitDir, WorkspaceRoot: rootPath})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := projects.FindWorkspaceByGitDir(context.Background(), gitDir)
	if err != nil {
		t.Fatal(err)
	}
	projects.Close()
	pellets, err := sqlite.OpenPelletRepository(context.Background(), first.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	pellet, err := pellets.CreatePellet(context.Background(), selected, storage.NewPellet{Title: "Independent linked worktree"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = pellets.TransitionPellet(context.Background(), selected, pellet.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if err != nil {
		t.Fatal(err)
	}
	pellets.Close()
	_, err = supervisor.options.Settings.Save(context.Background(), first.Database, storage.SaveWorkspaceRunSettingsRequest{WorkspaceID: selected.Workspace.ID, Settings: storage.CodexRunSettings{Executable: executable, Model: "future-model"}})
	if err != nil {
		t.Fatal(err)
	}
	second := ExecutionRequest{Database: first.Database, Selected: selected, Capture: storage.RunCapture{ProjectID: selected.Project.ID, WorkspaceID: selected.Workspace.ID, PelletNumber: pellet.Reference.Number, Mode: "run_one"}}
	ready := make(chan *WorkspaceExecution, 2)
	a, err := supervisor.Start(context.Background(), first, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, a)
	b, err := supervisor.Start(context.Background(), second, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, b)
	a.Stop()
	b.Stop()
	firstRun, err := a.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := b.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstRun.Settings.Codex.Model != "test-model" || secondRun.Settings.Codex.Model != "future-model" {
		t.Fatalf("crossed settings: %#v %#v", firstRun.Settings, secondRun.Settings)
	}
	for _, root := range []string{first.Database.Root, secondRoot} {
		canonical, _ := filepath.EvalSymlinks(root)
		events := readPeerEvents(t, root)
		for _, event := range events {
			if event.CWD != canonical {
				t.Fatalf("crossed linked cwd: %#v", event)
			}
		}
		requireStoppedPeers(t, events)
	}
}

func TestSupervisorHTTPDisconnectDoesNotOwnRunLifetime(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	ready := make(chan *WorkspaceExecution, 1)
	handles := make(chan *ExecutionHandle, 1)
	disconnected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle, err := supervisor.Start(r.Context(), request, holdingDriver(ready))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		handles <- handle
		fmt.Fprintln(w, "accepted")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatal(err)
	}
	handle := <-handles
	execution := awaitExecution(t, ready, handle)
	cancel()
	response.Body.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("HTTP did not disconnect")
	}
	if _, err := execution.Call(context.Background(), codex.ThreadRead, map[string]any{"threadId": "thread"}); err != nil {
		t.Fatalf("HTTP cancellation stopped work: %v", err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	requireStoppedPeers(t, readPeerEvents(t, request.Database.Root))
}

func TestSupervisorCrashHelper(t *testing.T) {
	encoded := os.Getenv("PELLETS_SUPERVISOR_CRASH_REQUEST")
	if encoded == "" {
		return
	}
	var request ExecutionRequest
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		t.Fatal(err)
	}
	supervisor := NewExecutionSupervisor(context.Background(), SupervisorOptions{
		Recorder: ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
			return sqlite.OpenExecutionRunDatabase(ctx, path)
		}},
		Settings: WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
			return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
		}},
	})
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, handle)
	fmt.Println("ready")
	<-handle.done
}

func TestSupervisorAbruptServerDeathStopsOwnedWorkAndFencesRestart(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestSupervisorCrashHelper$")
	command.Env = append(os.Environ(), "PELLETS_SUPERVISOR_CRASH_REQUEST="+string(encoded))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			line <- scanner.Text()
		} else {
			line <- ""
		}
	}()
	select {
	case value := <-line:
		if value != "ready" {
			t.Fatalf("crash helper: %q", value)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("crash helper not ready")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	requireStoppedPeers(t, readPeerEvents(t, request.Database.Root))
	identity, err := discovery.FindGitIdentity(context.Background(), request.Database.Root)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := executionlock.Acquire(identity.GitDir)
		if lock != nil {
			lock.Close()
			t.Fatal("crash silently allowed replay")
		}
		code := domain.PublicError(err).Code
		if code == "workspace_execution_recovery_required" {
			break
		}
		if code != "workspace_execution_busy" || time.Now().After(deadline) {
			t.Fatalf("crash lock: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err = supervisor.Start(context.Background(), request, holdingDriver(make(chan *WorkspaceExecution, 1)))
	if domain.PublicError(err).Code != "workspace_execution_recovery_required" {
		t.Fatalf("crash replay: %v", err)
	}
	store, err := sqlite.OpenExecutionRunDatabase(context.Background(), request.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListWorkspaceRuns(context.Background(), request.Selected.Workspace.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ThreadID != "thread" || runs[0].TurnID != "turn" || runs[0].State != "running" {
		t.Fatalf("lost crash evidence: %#v", runs)
	}
}

func TestSupervisorFinalPersistenceFailureStopsChildrenAndKeepsFence(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	open := supervisor.options.Recorder.Open
	var fail atomic.Bool
	supervisor.options.Recorder.Open = func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		if fail.Load() {
			return nil, errors.New("injected unavailable evidence store")
		}
		return open(ctx, path)
	}
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	execution := awaitExecution(t, ready, handle)
	before, err := execution.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	handle.Stop()
	if _, err := handle.Result(context.Background()); err == nil {
		t.Fatal("lost final save failure")
	}
	requireStoppedPeers(t, readPeerEvents(t, request.Database.Root))
	fail.Store(false)
	after, err := execution.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID || after.State != "running" {
		t.Fatalf("invented persisted interruption: %#v", after)
	}
	identity, err := discovery.FindGitIdentity(context.Background(), request.Database.Root)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := executionlock.Acquire(identity.GitDir)
	if lock != nil {
		lock.Close()
		t.Fatal("lost cleanup fence after save failure")
	}
	if domain.PublicError(err).Code != "workspace_execution_recovery_required" || domain.PublicError(err).Details["run_id"] != before.ID {
		t.Fatalf("wrong failure receipt: %v", err)
	}
}

func TestSupervisorDoesNotStopUnrelatedCodexProcess(t *testing.T) {
	executable := installSupervisorPeer(t)
	unrelated := exec.Command(executable, "--fake-descendant", "unrelated")
	unrelated.Dir = t.TempDir()
	testcodex.Detach(unrelated)
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { unrelated.Process.Kill(); unrelated.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(unrelated.Dir, "fake-unrelated-ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unrelated peer did not start")
		}
		time.Sleep(time.Millisecond)
	}
	supervisor, request := supervisorFixture(t, executable)
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, handle)
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if !testcodex.Alive(unrelated.Process.Pid) {
		t.Fatal("supervisor stopped an unrelated session of the same executable")
	}
	requireStoppedPeers(t, readPeerEvents(t, request.Database.Root))
}

func TestSupervisorAcceptedSettingsAndFiltersAreIndependentSnapshots(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	prepare := supervisor.options.Prepare
	gate := make(chan struct{})
	supervisor.options.Prepare = func(ctx context.Context, options codex.PrepareOptions) (*codex.PreparedRun, error) {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return prepare(ctx, options)
	}
	model, group := "test-model", "original-group"
	request.Overrides.Model = &model
	request.Capture.Group = &group
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	model, group = "mutated-model", "mutated-group"
	close(gate)
	execution := awaitExecution(t, ready, handle)
	run, err := execution.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.Settings.Codex.Model != "test-model" || run.Group == nil || *run.Group != "original-group" {
		t.Fatalf("accepted request changed: %#v", run)
	}
	handle.Stop()
	if _, err := handle.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
}
