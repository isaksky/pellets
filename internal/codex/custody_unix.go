//go:build darwin || linux

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const custodianArgument = "--pellets-internal-process-custodian"

type custodyReply struct {
	Finished bool   `json:"finished,omitempty"`
	Error    string `json:"error,omitempty"`
}

type processCustodian struct {
	mu       sync.Mutex
	control  *os.File
	decoder  *json.Decoder
	finished bool
	closed   bool
	err      error
}

// This private re-exec runs before CLI/test dispatch. No command line alone can
// start work: fd 3 must be our duplex socket and fd 4 an inherited locked file.
// The custodian owns no queue policy, credentials, or persistent service.
func init() {
	if len(os.Args) > 2 && os.Args[1] == custodianArgument {
		os.Exit(runProcessCustodian())
	}
}

func runProcessCustodian() int {
	if err := unix.SetNonblock(3, true); err != nil {
		return 125
	}
	control := os.NewFile(3, "pellets-custody")
	lock := os.NewFile(4, "pellets-execution-lock")
	if control == nil || lock == nil {
		return 125
	}
	if _, err := unix.GetsockoptInt(3, unix.SOL_SOCKET, unix.SO_TYPE); err != nil {
		return 125
	}
	if info, err := lock.Stat(); err != nil || !info.Mode().IsRegular() {
		return 125
	}
	unix.CloseOnExec(3)
	unix.CloseOnExec(4)
	defer control.Close()
	defer lock.Close()
	// Terminal/second-interrupt signals belong to the foreground server. Its
	// socket disappearing requests cleanup even after SIGKILL or a panic.
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	encoder := json.NewEncoder(control)
	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	tree, err := startProcessTree(cmd)
	if err != nil {
		_ = encoder.Encode(custodyReply{Finished: true, Error: "start owned process failed"})
		return 125
	}
	_ = encoder.Encode(custodyReply{})
	commands := make(chan byte)
	go func() {
		defer close(commands)
		var b [1]byte
		for {
			if _, err := control.Read(b[:]); err != nil {
				return
			}
			commands <- b[0]
		}
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var exitErr error
	rootExited := false
loop:
	for {
		select {
		case operation, ok := <-commands:
			if !ok || operation != 'r' {
				break loop
			}
			if err := tree.refresh(); err != nil {
				break loop
			}
			if err := encoder.Encode(custodyReply{}); err != nil {
				break loop
			}
		case exitErr = <-exited:
			rootExited = true
			break loop
		}
	}
	cleanupErr := tree.terminate()
	exitErr, cleanupErr = settleCustodian(exited, rootExited, exitErr, cleanupErr, tree.confirmStopped, func(reply custodyReply) {
		if control.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
			return
		}
		_ = encoder.Encode(reply)
	}, processSettlementTimeout)
	if cleanupErr != nil {
		return 125
	}
	if exitErr != nil {
		var status *exec.ExitError
		if errors.As(exitErr, &status) && status.ExitCode() > 0 {
			return status.ExitCode()
		}
		return 1
	}
	return 0
}

// Report uncertainty to the foreground process within a bound, but retain the
// custodian (and its inherited execution lock) until the root is reaped AND
// every owned identity is confirmed stopped. An uninterruptible process may
// outlive the parent shutdown deadline; it must not enable duplicate execution.
func settleCustodian(exited <-chan error, rootExited bool, exitErr, cleanupErr error, confirmed func() bool, report func(custodyReply), timeout time.Duration) (error, error) {
	if cleanupErr == nil && !rootExited {
		select {
		case exitErr = <-exited:
			rootExited = true
		case <-time.After(timeout):
			cleanupErr = errors.New("owned process was not reaped before the settlement deadline")
		}
	}
	if cleanupErr != nil {
		report(custodyReply{Error: "owned process cleanup could not be confirmed; execution lock remains held"})
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		if rootExited {
			exited = nil
		}
		for {
			stopped := confirmed()
			if rootExited && stopped {
				break
			}
			select {
			case exitErr = <-exited:
				rootExited = true
				exited = nil
			case <-ticker.C:
			}
		}
	}
	reply := custodyReply{Finished: true}
	if cleanupErr != nil {
		reply.Error = "owned process cleanup required delayed settlement"
	}
	report(reply)
	return exitErr, cleanupErr
}

func startOwnedProcess(ctx context.Context, cmd *exec.Cmd) (*processTree, error) {
	lock, _ := ctx.Value(custodyKey{}).(*os.File)
	if lock == nil {
		return startProcessTree(cmd)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	// NewFile only enables Go's deadline-aware poller for an already
	// nonblocking socket. Ignoring this would make SetReadDeadline ineffective.
	if err := errors.Join(unix.SetNonblock(pair[0], true), unix.SetNonblock(pair[1], true)); err != nil {
		unix.Close(pair[0])
		unix.Close(pair[1])
		return nil, err
	}
	parent, child := os.NewFile(uintptr(pair[0]), "pellets-custody-parent"), os.NewFile(uintptr(pair[1]), "pellets-custody-child")
	defer child.Close()
	args := append([]string{executable, custodianArgument, cmd.Path}, cmd.Args[1:]...)
	cmd.Path, cmd.Args = executable, args
	cmd.ExtraFiles = []*os.File{child, lock}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		parent.Close()
		return nil, err
	}
	g := &processCustodian{control: parent, decoder: json.NewDecoder(parent)}
	if err := g.receive(); err != nil || g.finished {
		parent.Close()
		reaped := make(chan error, 1)
		go func() { reaped <- cmd.Wait() }()
		var settlementErr error
		select {
		case <-reaped:
		case <-time.After(processSettlementTimeout):
			settlementErr = errors.New("failed process custodian did not settle before the deadline; its execution lock remains held")
		}
		return nil, errors.Join(ErrCleanup, fmt.Errorf("start owned process custodian failed"), err, settlementErr)
	}
	return &processTree{custodian: g}, nil
}

func (p *processTree) hasCustodian() bool { return p.custodian != nil }

// All socket exchanges are serialized. A terminal reply may arrive before a
// refresh request when the runtime exits; retain it for Close rather than
// mistaking a clean, already-reaped process for a failed control channel.
func (g *processCustodian) receive() error {
	if err := g.control.SetReadDeadline(time.Now().Add(6 * time.Second)); err != nil {
		g.err = fmt.Errorf("set custodian settlement deadline: %w", err)
		return g.err
	}
	var reply custodyReply
	if err := g.decoder.Decode(&reply); err != nil {
		g.err = fmt.Errorf("owned process custodian unavailable: %w", err)
		return g.err
	}
	if reply.Error != "" {
		g.err = errors.New(reply.Error)
	}
	if reply.Finished {
		g.finished = true
	}
	return g.err
}

func (g *processCustodian) refresh() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.finished || g.closed {
		return g.err
	}
	if err := g.control.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		g.err = fmt.Errorf("set custodian control deadline: %w", err)
		return g.err
	}
	// Read the terminal reply even if an already-exited custodian rejects the
	// write: it may have finished cleanup successfully before this refresh.
	_, _ = g.control.Write([]byte{'r'})
	return g.receive()
}

func (g *processCustodian) terminate() (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(ErrCleanup, err)
		}
	}()
	g.mu.Lock()
	defer g.mu.Unlock()
	defer func() { g.closed = true; g.control.Close() }()
	if g.finished || g.closed {
		return g.err
	}
	if err := g.control.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		g.err = fmt.Errorf("set custodian shutdown deadline: %w", err)
		return g.err
	}
	_, _ = g.control.Write([]byte{'s'})
	if err := g.receive(); err != nil {
		return err
	}
	if !g.finished {
		g.err = errors.New("owned process custodian did not confirm cleanup")
		return g.err
	}
	return g.err
}
