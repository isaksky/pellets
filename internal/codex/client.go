package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

type reply struct {
	result json.RawMessage
	err    error
}
type outbound struct {
	ctx  context.Context
	data []byte
	ack  chan error
}

// Client owns one child process. The server's final turn outcome and its process
// exit are separate: inspect LatestCompletion and drain Events even if Wait fails.
type Client struct {
	cfg                      Config
	runtime                  Runtime
	cmd                      *exec.Cmd
	tree                     *processTree
	stdin, stdout            *os.File
	stderr                   *tailBuffer
	events                   chan Event
	writes                   chan outbound
	done, finished           chan struct{}
	mu                       sync.Mutex
	nextID                   uint64
	pending                  map[string]chan reply
	requests                 map[string]bool
	completion               *Event
	err, exitErr, cleanupErr error
	once                     sync.Once
}

var _ Session = (*Client)(nil)

// Start verifies the executable's stable generated schema before spawning its
// app-server, then completes initialize/initialized. No account or model calls
// are made. The supplied context owns the process until Close or cancellation.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg, err := normalize(cfg)
	if err != nil {
		return nil, err
	}
	info, err := inspect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, runtime: info, cmd: command(cfg, "app-server", "--listen", "stdio://"),
		stderr: &tailBuffer{limit: cfg.StderrBytes}, events: make(chan Event, cfg.EventBuffer),
		writes: make(chan outbound, cfg.MaxPending), done: make(chan struct{}), finished: make(chan struct{}),
		pending: make(map[string]chan reply), requests: make(map[string]bool)}
	input, stdin, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdout, output, err := os.Pipe()
	if err != nil {
		input.Close()
		stdin.Close()
		return nil, err
	}
	c.stdin, c.stdout = stdin, stdout
	c.cmd.Stdin, c.cmd.Stdout, c.cmd.Stderr = input, output, c.stderr
	c.tree, err = startProcessTree(c.cmd)
	if err != nil {
		input.Close()
		stdin.Close()
		stdout.Close()
		output.Close()
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	input.Close()
	output.Close()
	readDone, writeDone, exitDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { defer close(readDone); defer close(c.events); c.readLoop() }()
	go func() { defer close(writeDone); c.writeLoop() }()
	go func() {
		err := c.cmd.Wait()
		c.mu.Lock()
		c.exitErr = err
		c.mu.Unlock()
		close(exitDone)
		// Drain stdout before ending the session, including a terminal event sent
		// immediately before exit. Bound inherited-pipe leaks from descendants.
		select {
		case <-readDone:
		case <-time.After(time.Second):
			c.stop(fmt.Errorf("Codex exited but stdout remained open: %w", io.ErrUnexpectedEOF))
		}
	}()
	go func() { <-exitDone; <-readDone; <-writeDone; close(c.finished) }()
	go func() {
		select {
		case <-ctx.Done():
			c.stop(ctx.Err())
		case <-c.done:
		}
	}()
	initCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := c.call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "pellets", "title": "Pellets", "version": cfg.ClientVersion},
		"capabilities": map[string]any{"experimentalApi": false},
	})
	if err == nil {
		var initialized struct {
			UserAgent string `json:"userAgent"`
		}
		if json.Unmarshal(result, &initialized) != nil || initialized.UserAgent == "" {
			err = fmt.Errorf("%w: initialize result is missing userAgent", ErrProtocol)
		} else {
			c.runtime.UserAgent = initialized.UserAgent
		}
	}
	if err == nil {
		err = c.send(initCtx, map[string]any{"method": "initialized"})
	}
	if err != nil {
		c.stop(err)
		<-c.finished
		return nil, fmt.Errorf("initialize %s: %w (stderr: %s)", info.Version, errors.Join(err, c.cleanupErr), c.Stderr())
	}
	return c, nil
}

func (c *Client) Runtime() Runtime     { return c.runtime }
func (c *Client) Stderr() string       { return c.stderr.String() }
func (c *Client) Events() <-chan Event { return c.events }

func (c *Client) LatestCompletion() *Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.completion == nil {
		return nil
	}
	e := *c.completion
	e.Params = append(json.RawMessage(nil), e.Params...)
	return &e
}

// Call cancellation stops local waiting, not the server operation. A response
// arriving later is discarded. To stop model work, explicitly Call TurnInterrupt
// with threadId and turnId and await turn/completed (status interrupted).
func (c *Client) Call(ctx context.Context, op Operation, params any) (json.RawMessage, error) {
	if _, ok := requiredOperations[op]; !ok {
		return nil, fmt.Errorf("%w: operation %s is outside the Pellets adapter", ErrUnsupported, op)
	}
	return c.call(ctx, string(op), params)
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	if len(c.pending) >= c.cfg.MaxPending {
		c.mu.Unlock()
		return nil, ErrBufferFull
	}
	c.nextID++
	id := strconv.FormatUint(c.nextID, 10)
	key := "n:" + id
	ch := make(chan reply, 1)
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	if params == nil {
		params = map[string]any{}
	}
	if err := c.send(ctx, map[string]any{"id": json.RawMessage(id), "method": method, "params": params}); err != nil {
		select {
		case r := <-ch:
			return r.result, r.err
		default:
			return nil, err
		}
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		// A correlated response received just before EOF wins over transport exit.
		select {
		case r := <-ch:
			return r.result, r.err
		default:
			return nil, c.failure()
		}
	}
}

// Respond answers an outstanding server request once. A non-nil rpcError sends
// an error instead of a result (for example -32601 for an unknown request).
// Failed/canceled writes end the session because delivery may be ambiguous.
func (c *Client) Respond(ctx context.Context, id json.RawMessage, result any, rpcError *RPCError) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := requestKey(id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return err
	}
	if responding, ok := c.requests[key]; !ok || responding {
		c.mu.Unlock()
		return fmt.Errorf("no unanswered Codex server request with ID %s", id)
	}
	c.requests[key] = true
	c.mu.Unlock()
	message := map[string]any{"id": id, "result": result}
	if rpcError != nil {
		delete(message, "result")
		message["error"] = rpcError
	}
	if err := c.send(ctx, message); err != nil {
		c.stop(err)
		return err
	}
	c.mu.Lock()
	delete(c.requests, key)
	c.mu.Unlock()
	return nil
}

func (c *Client) send(ctx context.Context, message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > c.cfg.MaxMessageBytes {
		return fmt.Errorf("%w: outbound message exceeds %d bytes", ErrBufferFull, c.cfg.MaxMessageBytes)
	}
	w := outbound{ctx: ctx, data: append(data, '\n'), ack: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.failure()
	case c.writes <- w:
	}
	select {
	case err := <-w.ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.failure()
	}
}

func (c *Client) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case w := <-c.writes:
			if err := w.ctx.Err(); err != nil {
				w.ack <- err
				continue
			}
			// os.Pipe write deadlines are not portable across all supported targets.
			// Closing the pipe on timeout unblocks Write without a leaked goroutine.
			timer := time.AfterFunc(15*time.Second, func() { c.stop(fmt.Errorf("Codex stdin write timed out")) })
			_, err := c.stdin.Write(w.data)
			timer.Stop()
			w.ack <- err
			if err != nil {
				c.stop(fmt.Errorf("write Codex stdin: %w", err))
				return
			}
		}
	}
}

func (c *Client) readLoop() {
	r := bufio.NewReaderSize(c.stdout, min(c.cfg.MaxMessageBytes+1, 64<<10))
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > c.cfg.MaxMessageBytes+1 {
			c.stop(fmt.Errorf("%w: inbound message exceeds %d bytes", ErrBufferFull, c.cfg.MaxMessageBytes))
			return
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if len(line) != 0 {
				err = fmt.Errorf("%w: truncated JSONL frame", ErrProtocol)
			}
			c.stop(fmt.Errorf("read Codex stdout: %w", err))
			return
		}
		if err := c.receive(bytes.TrimSuffix(line, []byte{'\n'})); err != nil {
			c.stop(err)
			return
		}
		line = nil
	}
}

func (c *Client) receive(line []byte) error {
	if err := c.tree.refresh(); err != nil {
		return fmt.Errorf("track Codex descendants: %w", err)
	}
	var msg map[string]json.RawMessage
	if !utf8.Valid(line) || json.Unmarshal(line, &msg) != nil || msg == nil {
		return ErrProtocol
	}
	id, hasID := msg["id"]
	methodRaw, hasMethod := msg["method"]
	result, hasResult := msg["result"]
	errorRaw, hasError := msg["error"]
	var key string
	var err error
	if hasID {
		key, err = requestKey(id)
		if err != nil {
			return err
		}
	}
	if hasMethod {
		var method string
		if json.Unmarshal(methodRaw, &method) != nil || method == "" || hasResult || hasError {
			return ErrProtocol
		}
		e := Event{Method: method, Params: msg["params"], ID: id}
		c.mu.Lock()
		if hasID {
			if _, exists := c.requests[key]; exists {
				c.mu.Unlock()
				return fmt.Errorf("%w: duplicate server request ID", ErrProtocol)
			}
			if len(c.requests) >= c.cfg.MaxPending {
				c.mu.Unlock()
				return ErrBufferFull
			}
			c.requests[key] = false
		} else if method == "turn/completed" {
			var completion struct {
				ThreadID string `json:"threadId"`
				Turn     struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"turn"`
			}
			if json.Unmarshal(e.Params, &completion) != nil || completion.ThreadID == "" || completion.Turn.ID == "" || (completion.Turn.Status != "completed" && completion.Turn.Status != "interrupted" && completion.Turn.Status != "failed") {
				c.mu.Unlock()
				return fmt.Errorf("%w: invalid turn/completed outcome", ErrProtocol)
			}
			copyEvent := e
			copyEvent.Params = append(json.RawMessage(nil), e.Params...)
			c.completion = &copyEvent
		} else if method == "serverRequest/resolved" {
			var resolved struct {
				RequestID json.RawMessage `json:"requestId"`
			}
			if json.Unmarshal(e.Params, &resolved) != nil {
				c.mu.Unlock()
				return ErrProtocol
			}
			resolvedKey, err := requestKey(resolved.RequestID)
			if err != nil {
				c.mu.Unlock()
				return err
			}
			delete(c.requests, resolvedKey)
		}
		c.mu.Unlock()
		select {
		case c.events <- e:
			return nil
		default:
			return ErrBufferFull
		}
	}
	if !hasID || hasResult == hasError {
		return ErrProtocol
	}
	r := reply{result: result}
	if hasError {
		var fields map[string]json.RawMessage
		var rpcError RPCError
		if json.Unmarshal(errorRaw, &fields) != nil || fields["code"] == nil || fields["message"] == nil || bytes.Equal(bytes.TrimSpace(fields["code"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(fields["message"]), []byte("null")) || json.Unmarshal(errorRaw, &rpcError) != nil {
			return ErrProtocol
		}
		r.err = &rpcError
	}
	c.mu.Lock()
	if ch, ok := c.pending[key]; ok {
		delete(c.pending, key)
		ch <- r
	}
	c.mu.Unlock()
	return nil // Late responses to locally canceled requests are harmless.
}

func requestKey(id json.RawMessage) (string, error) {
	id = bytes.TrimSpace(id)
	var s string
	if json.Unmarshal(id, &s) == nil && string(id) != "null" {
		return "s:" + s, nil
	}
	var n int64
	if json.Unmarshal(id, &n) == nil && string(id) != "null" {
		return "n:" + strconv.FormatInt(n, 10), nil
	}
	return "", fmt.Errorf("%w: request ID must be an integer or string", ErrProtocol)
}

func (c *Client) failure() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *Client) stop(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		// Freeze/discover the owned Unix tree before closing stdin can make the
		// leader exit and orphan children that just started in another session.
		cleanupErr := c.tree.terminate()
		if cleanupErr != nil {
			_ = c.cmd.Process.Kill()
		}
		c.stdin.Close()
		c.stdout.Close()
		c.mu.Lock()
		c.cleanupErr = cleanupErr
		c.mu.Unlock()
	})
}

// Wait waits for process reaping and reader/writer completion. An unexpected
// clean exit still reports EOF; a turn's success is in turn/completed, not exit 0.
func (c *Client) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.finished:
		c.mu.Lock()
		defer c.mu.Unlock()
		return errors.Join(c.err, c.exitErr, c.cleanupErr)
	}
}

// Close forcibly ends this owned app-server process family, waits for the
// app-server to be reaped, and reports any process-family cleanup failure.
// A runner should interrupt an active turn and observe completion before Close
// when it needs a graceful persisted interruption outcome.
func (c *Client) Close() error {
	c.stop(ErrClosed)
	<-c.finished
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cleanupErr
}
