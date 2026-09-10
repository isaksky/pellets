//go:build darwin || linux

package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"pellets/internal/domain"
	"pellets/internal/executionlock"
)

func TestCustodianUnsettledRoot(t *testing.T) {
	if os.Getenv("PELLETS_CUSTODY_UNSETTLED_ROOT") != "1" {
		return
	}
	fmt.Println("ready")
	time.Sleep(time.Minute)
}

func TestCustodianCleanupUncertaintyBoundsCloseAndRetainsExecutionLock(t *testing.T) {
	dir := t.TempDir()
	lock, err := executionlock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lock.Record(executionlock.Owner{Database: "test.db", RunID: 42}); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Dup(int(lock.File().Fd()))
	if err != nil {
		t.Fatal(err)
	}
	unix.CloseOnExec(fd)
	held := os.NewFile(uintptr(fd), "custodian-retained-lock")
	defer held.Close()
	root := exec.Command(os.Args[0], "-test.run=^TestCustodianUnsettledRoot$")
	root.Env = append(os.Environ(), "PELLETS_CUSTODY_UNSETTLED_ROOT=1", "GORACE=atexit_sleep_ms=0")
	stdout, err := root.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := startProcessTree(root)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.terminate()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("unsettled root failed to start")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unsettled root did not become ready")
	}
	reaped := make(chan error, 1)
	go func() { reaped <- root.Wait() }()
	parent, child := custodyTestSocket(t)
	finished := make(chan struct{})
	go func() {
		defer func() { child.Close(); held.Close(); close(finished) }()
		var operation [1]byte
		if _, err := child.Read(operation[:]); err != nil || operation[0] != 's' {
			return
		}
		// Simulate an OS termination failure deterministically: the real root
		// stays alive until the test explicitly releases it below. This is the
		// same settlement path used by the re-executed production custodian.
		settleCustodian(reaped, false, nil, errors.New("injected termination failure"), tree.confirmStopped, func(reply custodyReply) {
			child.SetWriteDeadline(time.Now().Add(time.Second))
			_ = json.NewEncoder(child).Encode(reply)
		}, 20*time.Millisecond)
	}()
	defer func() {
		tree.terminate()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("custodian failed to finish after test cleanup")
		}
	}()
	client := custodyTestClient(t, parent, finished)
	started := time.Now()
	if err := client.Close(); !errors.Is(err, ErrCleanup) {
		t.Fatalf("unsettled close = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("Close waited for an uninterruptible root")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if other, err := executionlock.Acquire(dir); domain.PublicError(err).Code != "workspace_execution_busy" {
		if other != nil {
			other.Close()
		}
		t.Fatalf("released exclusion before cleanup: %v", err)
	}
	identity, err := lookupProcess(root.Process.Pid)
	if err != nil || identity.zombie {
		t.Fatalf("uncertain Close killed or lost the retained root: %#v %v", identity, err)
	}
	select {
	case <-finished:
		t.Fatal("custodian settled while root remained active")
	default:
	}
	if err := client.Close(); !errors.Is(err, ErrCleanup) {
		t.Fatalf("repeated Close lost uncertainty: %v", err)
	}
	if err := tree.terminate(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("custodian did not release after actual root settlement")
	}
	if root.ProcessState == nil {
		t.Fatal("root was not reaped")
	}
	if other, err := executionlock.Acquire(dir); domain.PublicError(err).Code != "workspace_execution_recovery_required" {
		if other != nil {
			other.Close()
		}
		t.Fatalf("lost explicit recovery fence: %v", err)
	}
	if err := client.Wait(context.Background()); !errors.Is(err, ErrCleanup) {
		t.Fatalf("late settlement erased cleanup evidence: %v", err)
	}
}

func TestCustodianReapingDeadlineReportsBeforeRootAndDescendantsSettle(t *testing.T) {
	exited := make(chan error, 1)
	replies := make(chan custodyReply, 2)
	done := make(chan struct{})
	var descendantsStopped atomic.Bool
	go func() {
		defer close(done)
		settleCustodian(exited, false, nil, nil, descendantsStopped.Load, func(reply custodyReply) { replies <- reply }, 20*time.Millisecond)
	}()
	select {
	case reply := <-replies:
		if reply.Finished || reply.Error == "" {
			t.Fatalf("did not report unsettled root: %#v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("root reaping blocked uncertainty report")
	}
	exited <- nil
	select {
	case <-done:
		t.Fatal("root exit alone released descendant custody")
	case <-time.After(30 * time.Millisecond):
	}
	descendantsStopped.Store(true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("settled descendants did not end custody")
	}
	reply := <-replies
	if !reply.Finished || reply.Error == "" {
		t.Fatalf("late settlement lost cleanup uncertainty: %#v", reply)
	}
}

func TestClientCloseBoundsSettlementEvenAfterCleanupAcknowledgement(t *testing.T) {
	parent, child := custodyTestSocket(t)
	defer child.Close()
	finished := make(chan struct{})
	client := custodyTestClient(t, parent, finished)
	client.tree.custodian.finished = true
	started := time.Now()
	if err := client.Close(); !errors.Is(err, ErrCleanup) {
		t.Fatalf("unreaped acknowledged process: %v", err)
	}
	if time.Since(started) > processSettlementTimeout+time.Second {
		t.Fatal("parent settlement wait exceeded its bound")
	}
	close(finished)
	if err := client.Close(); !errors.Is(err, ErrCleanup) {
		t.Fatalf("late exit erased close timeout: %v", err)
	}
}

func custodyTestSocket(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	if err := errors.Join(unix.SetNonblock(pair[0], true), unix.SetNonblock(pair[1], true)); err != nil {
		unix.Close(pair[0])
		unix.Close(pair[1])
		t.Fatal(err)
	}
	parent, child := os.NewFile(uintptr(pair[0]), "custody-test-parent"), os.NewFile(uintptr(pair[1]), "custody-test-child")
	t.Cleanup(func() { parent.Close(); child.Close() })
	return parent, child
}

func custodyTestClient(t *testing.T, control *os.File, finished chan struct{}) *Client {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { read.Close(); write.Close() })
	return &Client{tree: &processTree{custodian: &processCustodian{control: control, decoder: json.NewDecoder(control)}}, stdin: write, stdout: read, done: make(chan struct{}), finished: finished}
}
