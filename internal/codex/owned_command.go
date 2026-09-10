package codex

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

// RunOwnedCommand runs a local orchestration command under the same process
// containment as Codex. Use exec.Command, not CommandContext: cancellation must
// settle the entire owned tree, including hooks, filters, and signing children.
// A WithExecutionLock context also retains custody through foreground death.
// Output is bounded and drained; stdout overflow is an error, never truncation
// presented as valid Git evidence. Raw stderr must be sanitized before storage.
func RunOwnedCommand(ctx context.Context, cmd *exec.Cmd) (stdout, stderr string, resultErr error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if cmd == nil || cmd.Cancel != nil {
		return "", "", errors.New("owned commands require exec.Command without independent cancellation")
	}
	out := &commandOutput{limit: 16 << 20}
	diagnostic := &commandStderr{limit: 4096}
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = out, diagnostic, time.Second
	tree, err := startOwnedProcess(ctx, cmd)
	if err != nil {
		return "", "", err
	}
	defer func() {
		resultErr = errors.Join(resultErr, tree.terminate())
		var overflow bool
		stdout, overflow = out.snapshot()
		stderr = diagnostic.String()
		if overflow {
			resultErr = errors.Join(resultErr, ErrBufferFull)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return "", "", err
	case <-ctx.Done():
		cleanupErr := tree.terminate()
		if cleanupErr != nil && !tree.hasCustodian() {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(processSettlementTimeout):
			cleanupErr = errors.Join(cleanupErr, ErrCleanup, errors.New("owned command did not settle before the close deadline"))
		}
		return "", "", errors.Join(ctx.Err(), cleanupErr)
	}
}

type commandOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	overflow bool
}

func (b *commandOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if remaining := b.limit - len(b.data); len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *commandOutput) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data), b.overflow
}

// Retaining an arbitrary stderr suffix could remove "token=" or an
// Authorization header while exposing its value to the later sanitizer.
// Once the capture wraps, discard the entire leading line: only subsequent
// lines have a known beginning. An overlong line without LF is omitted in full,
// including malformed UTF-8, carriage returns, and fragments across writes.
type commandStderr struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (b *commandStderr) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.data)+n > b.limit {
		b.truncated = true
	}
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
		return n, nil
	}
	if extra := len(b.data) + n - b.limit; extra > 0 {
		copy(b.data, b.data[extra:])
		b.data = b.data[:len(b.data)-extra]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *commandStderr) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated {
		return string(b.data)
	}
	const omitted = "Earlier stderr was truncated; incomplete leading line omitted.\n"
	if newline := bytes.IndexByte(b.data, '\n'); newline >= 0 {
		return omitted + string(b.data[newline+1:])
	}
	return omitted
}
