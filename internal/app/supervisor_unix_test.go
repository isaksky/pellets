//go:build darwin || linux

package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pellets/internal/codex"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
)

// Pause the exact owned custodian, not a system process or installed Codex.
// This deterministically exercises failed control/cleanup settlement through
// the real supervisor, adapter, persistence, OS lock, and fake process family.
func TestSupervisorUnresponsiveCustodianPersistsUncertaintyAndRetainsLock(t *testing.T) {
	executable := installSupervisorPeer(t)
	supervisor, request := supervisorFixture(t, executable)
	ready := make(chan *WorkspaceExecution, 1)
	handle, err := supervisor.Start(context.Background(), request, holdingDriver(ready))
	if err != nil {
		t.Fatal(err)
	}
	awaitExecution(t, ready, handle)
	events := readPeerEvents(t, request.Database.Root)
	rootPID := 0
	for _, event := range events {
		if event.Method == "process" {
			rootPID = event.PID
			break
		}
	}
	if rootPID == 0 {
		t.Fatal("owned fake Codex root is missing")
	}
	guardian := testProcessParent(t, rootPID)
	if guardian == os.Getpid() || testProcessParent(t, guardian) != os.Getpid() {
		t.Fatal("fake Codex is not owned by this test's custodian")
	}
	resumed := false
	defer func() {
		if !resumed {
			syscall.Kill(guardian, syscall.SIGCONT)
		}
	}()
	if err := syscall.Kill(guardian, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	handle.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	run, err := handle.Result(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("supervisor blocked forever on custodian settlement")
	}
	if !errors.Is(err, codex.ErrCleanup) || run.State != "needs_attention" || run.Outcome != "unknown" {
		t.Fatalf("unconfirmed cleanup evidence: %#v %v", run, err)
	}
	if err := supervisor.Close(); !errors.Is(err, codex.ErrCleanup) {
		t.Fatalf("foreground close lost cleanup failure: %v", err)
	}
	identity, err := discovery.FindGitIdentity(context.Background(), request.Database.Root)
	if err != nil {
		t.Fatal(err)
	}
	if lock, err := executionlock.Acquire(identity.GitDir); domain.PublicError(err).Code != "workspace_execution_busy" {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("custodian released exclusion prematurely: %v", err)
	}
	if err := syscall.Kill(guardian, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	resumed = true
	requireStoppedPeers(t, readPeerEvents(t, request.Database.Root))
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := executionlock.Acquire(identity.GitDir)
		if lock != nil {
			lock.Close()
			t.Fatal("late cleanup silently cleared explicit recovery fence")
		}
		if domain.PublicError(err).Code == "workspace_execution_recovery_required" {
			break
		}
		if domain.PublicError(err).Code != "workspace_execution_busy" || time.Now().After(deadline) {
			t.Fatalf("custodian did not settle: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, err := supervisor.options.Recorder.Read(context.Background(), request.Database, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "needs_attention" || stored.Outcome != "unknown" {
		t.Fatalf("late process exit rewrote uncertain evidence: %#v", stored)
	}
}

func testProcessParent(t *testing.T, pid int) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	parent, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatal(err)
	}
	return parent
}
