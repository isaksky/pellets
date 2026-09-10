package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain makes this test binary a deterministic stdio peer. Normal tests never
// launch an installed runtime, contact a model, or read credentials.
func TestMain(m *testing.M) {
	if mode := os.Getenv("PELLETS_CODEX_TEST_PEER"); mode != "" {
		peer(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeConfig(t *testing.T, mode string) Config {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Config{Executable: exe, Env: []string{"PELLETS_CODEX_TEST_PEER=" + mode, "GORACE=atexit_sleep_ms=0"}}
}

func startFake(t *testing.T, mode string, adjust func(*Config)) *Client {
	t.Helper()
	cfg := fakeConfig(t, mode)
	if adjust != nil {
		adjust(&cfg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func nextEvent(t *testing.T, c *Client) Event {
	t.Helper()
	select {
	case e, ok := <-c.Events():
		if !ok {
			t.Fatalf("events ended: %v; stderr %s", c.failure(), c.Stderr())
		}
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("event timed out")
		return Event{}
	}
}

func wait(t *testing.T, c *Client) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := c.Wait(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("process was not reaped")
	}
	return err
}

func TestHandshakeOperationsAndArgumentArrays(t *testing.T) {
	literal := `test="spaces ; $(printf unsafe) & more"`
	c := startFake(t, "echo", func(cfg *Config) { cfg.Arguments = []string{"--config", literal}; cfg.Dir = t.TempDir() })
	if c.Runtime().Version != "codex-cli 0.151.0" || c.Runtime().UserAgent != "fake-codex" {
		t.Fatal(c.Runtime())
	}
	for op := range requiredOperations {
		result, err := c.Call(context.Background(), op, map[string]any{"threadId": "thread", "input": []any{map[string]any{"type": "text", "text": "hello"}}})
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		var echoed struct {
			Method string
			Args   []string
			Params map[string]any
		}
		if err := json.Unmarshal(result, &echoed); err != nil {
			t.Fatal(err)
		}
		if echoed.Method != string(op) || echoed.Params["threadId"] != "thread" || !reflect.DeepEqual(echoed.Args, []string{"--config", literal, "app-server", "--listen", "stdio://"}) {
			t.Fatalf("%s: %s", op, result)
		}
	}
	if _, err := c.Call(context.Background(), "made/up", nil); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestConcurrentRequestsAreCorrelated(t *testing.T) {
	c := startFake(t, "reverse", nil)
	var wg sync.WaitGroup
	for _, value := range []string{"first", "second"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			result, err := c.Call(context.Background(), ThreadRead, map[string]any{"value": value})
			if err != nil {
				t.Error(err)
				return
			}
			var echoed struct{ Value string }
			if json.Unmarshal(result, &echoed) != nil || echoed.Value != value {
				t.Errorf("mismatched reply %s for %s", result, value)
			}
		}(value)
	}
	wg.Wait()
}

func TestInterleavedInteractions(t *testing.T) {
	c := startFake(t, "interactions", nil)
	done := make(chan error, 1)
	go func() { _, err := c.Call(context.Background(), TurnStart, nil); done <- err }()
	if e := nextEvent(t, c); e.Method != "future/optional" {
		t.Fatal(e)
	}
	for _, id := range []string{"1", `"question"`} {
		e := nextEvent(t, c)
		if string(e.ID) != id {
			t.Fatalf("ID: %s", e.ID)
		}
		if _, err := c.Call(context.Background(), AccountRead, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Respond(context.Background(), e.ID, map[string]any{"answers": map[string]any{"q": map[string]any{"answers": []string{"keep going"}}}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Respond(context.Background(), e.ID, nil, nil); err == nil {
			t.Fatal("accepted duplicate response")
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("turn start hung")
	}
}

func TestUnknownServerRequestCanBeRejected(t *testing.T) {
	c := startFake(t, "unknown-request", nil)
	go c.Call(context.Background(), TurnStart, nil)
	e := nextEvent(t, c)
	if err := c.Respond(context.Background(), e.ID, nil, &RPCError{Code: -32601, Message: "unsupported interaction"}); err != nil {
		t.Fatal(err)
	}
	if e := nextEvent(t, c); e.Method != "test/rejected" {
		t.Fatal(e)
	}
}

func TestResolvedRequestCannotBeAnswered(t *testing.T) {
	c := startFake(t, "resolved", nil)
	go c.Call(context.Background(), TurnStart, nil)
	e := nextEvent(t, c)
	if resolved := nextEvent(t, c); resolved.Method != "serverRequest/resolved" {
		t.Fatal(resolved)
	}
	if err := c.Respond(context.Background(), e.ID, nil, nil); err == nil {
		t.Fatal("answered a resolved request")
	}
}

func TestLocalCancellationAndRemoteInterrupt(t *testing.T) {
	c := startFake(t, "cancel", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Call(ctx, TurnStart, nil); done <- err }()
	if nextEvent(t, c).Method != "test/received" {
		t.Fatal("missing receipt")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	result, err := c.Call(context.Background(), TurnInterrupt, map[string]any{"threadId": "thread", "turnId": "turn"})
	if err != nil || string(result) != "{}" {
		t.Fatalf("interrupt: %s %v", result, err)
	}
	if nextEvent(t, c).Method != "turn/completed" {
		t.Fatal("missing terminal outcome")
	}
	if !strings.Contains(string(c.LatestCompletion().Params), "interrupted") {
		t.Fatal(c.LatestCompletion())
	}
}

func TestTerminalOutcomeSurvivesImmediateExit(t *testing.T) {
	for range 12 {
		c := startFake(t, "terminal-exit", nil)
		result, err := c.Call(context.Background(), TurnStart, nil)
		if err != nil || string(result) != "{}" {
			t.Fatalf("lost correlated reply: %s %v", result, err)
		}
		if err := wait(t, c); err == nil || !strings.Contains(err.Error(), "exit status 7") {
			t.Fatal(err)
		}
		completion := c.LatestCompletion()
		if completion == nil || !strings.Contains(string(completion.Params), "completed") {
			t.Fatal("lost terminal outcome")
		}
		completion.Params[0] = 'x'
		if c.LatestCompletion().Params[0] != '{' {
			t.Fatal("completion alias escaped")
		}
		if e := nextEvent(t, c); e.Method != "turn/completed" {
			t.Fatal(e)
		}
	}
}

func TestProtocolFailuresWakePendingRequests(t *testing.T) {
	for _, mode := range []string{"malformed", "truncated", "eof", "stdout-closed", "oversized", "bad-envelope", "bad-error", "bad-id", "invalid-utf8", "invalid-completion"} {
		t.Run(mode, func(t *testing.T) {
			c := startFake(t, mode, func(cfg *Config) { cfg.MaxMessageBytes = 1024 })
			_, err := c.Call(context.Background(), ThreadRead, nil)
			if err == nil {
				t.Fatal("accepted bad response")
			}
			if mode == "oversized" && !errors.Is(err, ErrBufferFull) {
				t.Fatal(err)
			}
			if mode != "oversized" && mode != "eof" && mode != "stdout-closed" && !errors.Is(err, ErrProtocol) {
				t.Fatal(err)
			}
			if wait(t, c) == nil {
				t.Fatal("missing terminal error")
			}
		})
	}
}

func TestBoundedBuffersAndStderrSeparation(t *testing.T) {
	t.Run("event overflow retains completion", func(t *testing.T) {
		c := startFake(t, "overflow", func(cfg *Config) { cfg.EventBuffer = 1 })
		_, _ = c.Call(context.Background(), TurnStart, nil)
		if err := wait(t, c); !errors.Is(err, ErrBufferFull) {
			t.Fatal(err)
		}
		if c.LatestCompletion() == nil {
			t.Fatal("overflow lost terminal outcome")
		}
	})
	t.Run("pending requests", func(t *testing.T) {
		c := startFake(t, "cancel", func(cfg *Config) { cfg.MaxPending = 1 })
		go c.Call(context.Background(), TurnStart, nil)
		nextEvent(t, c)
		if _, err := c.Call(context.Background(), ThreadRead, nil); !errors.Is(err, ErrBufferFull) {
			t.Fatal(err)
		}
	})
	t.Run("server requests", func(t *testing.T) {
		c := startFake(t, "server-overflow", func(cfg *Config) { cfg.MaxPending = 1 })
		_, _ = c.Call(context.Background(), TurnStart, nil)
		if err := wait(t, c); !errors.Is(err, ErrBufferFull) {
			t.Fatal(err)
		}
	})
	t.Run("stderr", func(t *testing.T) {
		c := startFake(t, "stderr", func(cfg *Config) { cfg.StderrBytes = 80 })
		if _, err := c.Call(context.Background(), ThreadRead, nil); err != nil {
			t.Fatal(err)
		}
		if got := c.Stderr(); len(got) > 80 || !strings.HasSuffix(got, "stderr-tail") {
			t.Fatalf("stderr tail: %q", got)
		}
	})
	t.Run("outbound frame", func(t *testing.T) {
		c := startFake(t, "echo", func(cfg *Config) { cfg.MaxMessageBytes = 1024 })
		if _, err := c.Call(context.Background(), TurnStart, strings.Repeat("x", 2048)); !errors.Is(err, ErrBufferFull) {
			t.Fatal(err)
		}
		if _, err := c.Call(context.Background(), AccountRead, nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRPCErrorPreservesData(t *testing.T) {
	c := startFake(t, "rpc-error", nil)
	_, err := c.Call(context.Background(), TurnSteer, nil)
	var rpcError *RPCError
	if !errors.As(err, &rpcError) || rpcError.Code != -32602 || string(rpcError.Data) != `{"expectedTurnId":"other"}` {
		t.Fatal(err)
	}
}

func TestStartupFailuresAndSchemaCompatibility(t *testing.T) {
	for _, mode := range []string{"bad-version", "missing-method", "missing-field", "schema-fail", "startup-fail", "bad-initialize"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := Start(ctx, fakeConfig(t, mode))
			if err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected immediate actionable failure: %v", err)
			}
			if (mode == "missing-method" || mode == "missing-field" || mode == "bad-version" || mode == "schema-fail") && !errors.Is(err, ErrUnsupported) {
				t.Fatal(err)
			}
		})
	}
	_, err := Start(context.Background(), Config{Executable: filepath.Join(t.TempDir(), "missing-codex")})
	if err == nil || !strings.Contains(err.Error(), "install Codex") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := Start(ctx, fakeConfig(t, "no-initialize")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestCancellationUnblocksFullStdinAndReapsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c, err := Start(ctx, fakeConfig(t, "no-read"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); c.Close() })
	done := make(chan error, 1)
	go func() { _, err := c.Call(context.Background(), TurnStart, strings.Repeat("x", 4<<20)); done <- err }()
	cancel()
	if err := wait(t, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write succeeded after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("write hung")
	}
}

func TestInstalledRuntime(t *testing.T) {
	if os.Getenv("PELLETS_CODEX_LIVE") != "1" {
		t.Skip("explicit installed-runtime smoke test only; no model turns")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Start(ctx, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// These only inspect local configuration. Do not start threads or model work.
	for _, op := range []Operation{ConfigRead, ConfigRequirementsRead} {
		if _, err := c.Call(ctx, op, map[string]any{}); err != nil {
			t.Fatalf("%s: %v", op, err)
		}
	}
	t.Logf("verified %s (%s), initialization and local configuration reads", c.Runtime().Version, c.Runtime().Executable)
}

func peer(mode string) {
	if mode == "descendant-child" || mode == "descendant-grandchild" {
		peerDescendant(mode)
		return
	}
	args := os.Args[1:]
	for len(args) >= 2 && (args[0] == "--config" || args[0] == "-c" || args[0] == "--enable" || args[0] == "--disable") {
		args = args[2:]
	}
	if len(args) == 1 && args[0] == "--version" {
		if strings.HasPrefix(mode, "probe-descendant") {
			spawnDescendant("descendant-grandchild", io.Discard)
			// The readiness file is a synchronization point: the probe must have
			// a running descendant before it exits or is canceled by the test.
			deadline := time.Now().Add(5 * time.Second)
			for {
				if data, _ := os.ReadFile(os.Getenv("PELLETS_TEST_DESCENDANT_FILE")); len(data) > 0 {
					break
				}
				if time.Now().After(deadline) {
					panic("descendant failed to start")
				}
				time.Sleep(time.Millisecond)
			}
			if mode == "probe-descendant-cancel" {
				time.Sleep(30 * time.Second)
				return
			}
		}
		if mode == "bad-version" {
			fmt.Println("other-runtime")
			return
		}
		fmt.Println("codex-cli 0.151.0")
		return
	}
	if len(args) == 4 && args[0] == "app-server" && args[1] == "generate-json-schema" && args[2] == "--out" {
		if mode == "schema-fail" {
			os.Exit(8)
		}
		peerSchema(args[3], mode)
		return
	}
	if !reflect.DeepEqual(args, []string{"app-server", "--listen", "stdio://"}) {
		panic("wrong stdio invocation")
	}
	if mode == "startup-fail" {
		fmt.Fprint(os.Stderr, "startup diagnostic")
		os.Exit(9)
	}
	if mode == "stderr" {
		fmt.Fprint(os.Stderr, strings.Repeat("not JSON\n", 10000)+"stderr-tail")
	}
	r := bufio.NewScanner(os.Stdin)
	r.Buffer(make([]byte, 4096), 10<<20)
	read := func() map[string]json.RawMessage {
		if !r.Scan() {
			os.Exit(0)
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(r.Bytes(), &m) != nil {
			panic("bad client JSON")
		}
		return m
	}
	write := func(v any) {
		if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
			os.Exit(0)
		}
	}
	response := func(id json.RawMessage, result any) { write(map[string]any{"id": id, "result": result}) }
	notify := func(method string, params any) { write(map[string]any{"method": method, "params": params}) }
	completed := func(status string) {
		notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": status}})
	}
	m := read()
	if string(m["method"]) != `"initialize"` || strings.Contains(string(m["params"]), `"experimentalApi":true`) {
		panic("missing stable initialize")
	}
	if mode == "no-initialize" {
		time.Sleep(30 * time.Second)
		return
	}
	if mode == "bad-initialize" {
		response(m["id"], map[string]any{})
		return
	}
	response(m["id"], map[string]any{"userAgent": "fake-codex"})
	if string(read()["method"]) != `"initialized"` {
		panic("missing initialized notification")
	}
	if mode == "no-read" {
		time.Sleep(30 * time.Second)
		return
	}
	var held map[string]json.RawMessage
	answers := 0
	for {
		m = read()
		var method string
		_ = json.Unmarshal(m["method"], &method)
		if strings.HasPrefix(mode, "descendants") {
			if method == "turn/start" {
				spawnDescendant("descendant-child", os.Stdout)
				response(m["id"], map[string]any{})
				continue
			}
			switch mode {
			case "descendants-malformed":
				fmt.Fprintln(os.Stdout, "{broken}")
			case "descendants-overflow":
				for range 5 {
					notify("test/overflow", nil)
				}
			case "descendants-exit":
				os.Exit(7)
			default:
				response(m["id"], map[string]any{})
			}
			continue
		}
		switch mode {
		case "reverse":
			if held == nil {
				held = m
				continue
			}
			response(m["id"], m["params"])
			response(held["id"], held["params"])
			held = nil
		case "interactions":
			if method == "turn/start" {
				held = m
				notify("future/optional", map[string]any{"newField": true})
				write(map[string]any{"id": 1, "method": "item/tool/requestUserInput", "params": map[string]any{}})
				write(map[string]any{"id": "question", "method": "item/tool/requestUserInput", "params": map[string]any{}})
			} else if method != "" {
				response(m["id"], map[string]any{})
			} else {
				if !strings.Contains(string(m["result"]), "keep going") {
					panic("wrong interaction response")
				}
				answers++
				if answers == 2 {
					response(held["id"], map[string]any{})
				}
			}
		case "unknown-request":
			if method != "" {
				write(map[string]any{"id": "unknown", "method": "future/request", "params": map[string]any{}})
			} else {
				if !strings.Contains(string(m["error"]), "-32601") || m["result"] != nil {
					panic("wrong rejection")
				}
				notify("test/rejected", nil)
			}
		case "resolved":
			write(map[string]any{"id": "gone", "method": "item/tool/requestUserInput"})
			notify("serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "gone"})
		case "cancel":
			if method == "turn/start" {
				held = m
				notify("test/received", nil)
			} else if method == "turn/interrupt" {
				if string(m["params"]) != `{"threadId":"thread","turnId":"turn"}` {
					panic("wrong interrupt params")
				}
				response(held["id"], map[string]any{})
				response(m["id"], map[string]any{})
				completed("interrupted")
			} else {
				panic("invented cancellation notification")
			}
		case "terminal-exit":
			response(m["id"], map[string]any{})
			completed("completed")
			os.Exit(7)
		case "overflow":
			notify("future/optional", nil)
			completed("failed")
		case "server-overflow":
			write(map[string]any{"id": "a", "method": "item/tool/requestUserInput"})
			write(map[string]any{"id": "b", "method": "item/tool/requestUserInput"})
		case "malformed":
			fmt.Fprintln(os.Stdout, "{not-json}")
		case "truncated":
			fmt.Fprint(os.Stdout, `{"id":`)
			return
		case "eof":
			return
		case "stdout-closed":
			os.Stdout.Close()
			time.Sleep(30 * time.Second)
			return
		case "oversized":
			fmt.Fprintln(os.Stdout, strings.Repeat("x", 2048))
		case "bad-envelope":
			write(map[string]any{"id": m["id"]})
		case "bad-error":
			write(map[string]any{"id": m["id"], "error": map[string]any{"code": nil, "message": nil}})
		case "bad-id":
			write(map[string]any{"id": nil, "result": nil})
		case "invalid-utf8":
			_, _ = os.Stdout.Write([]byte{'{', '"', 'x', '"', ':', '"', 255, '"', '}', '\n'})
		case "invalid-completion":
			notify("turn/completed", map[string]any{})
		case "rpc-error":
			write(map[string]any{"id": m["id"], "error": map[string]any{"code": -32602, "message": "turn mismatch", "data": map[string]any{"expectedTurnId": "other"}}})
		default:
			response(m["id"], map[string]any{"method": method, "params": m["params"], "args": os.Args[1:]})
		}
	}
}

func spawnDescendant(mode string, stdout io.Writer) {
	cmd := exec.Command(os.Args[0])
	if os.Getenv("PELLETS_TEST_DETACH") == "1" {
		detachTestDescendant(cmd)
	}
	cmd.Env = append(os.Environ(), "PELLETS_CODEX_TEST_PEER="+mode)
	cmd.Stdout, cmd.Stderr = stdout, os.Stderr
	if os.Getenv("PELLETS_TEST_DESCENDANT_FILE") != "" {
		cmd.Stderr = io.Discard
	}
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	// The fake peer deliberately doesn't wait: descendants outlive its normal
	// exit unless the adapter owns and terminates the process family.
}

func peerDescendant(mode string) {
	time.AfterFunc(15*time.Second, func() { os.Exit(0) }) // Bound leaks if a test fails.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	ready := map[string]any{"address": listener.Addr().String(), "pid": os.Getpid()}
	if path := os.Getenv("PELLETS_TEST_DESCENDANT_FILE"); path != "" {
		data, _ := json.Marshal(ready)
		if err := os.WriteFile(path, data, 0600); err != nil {
			panic(err)
		}
	} else {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"method": "test/descendant-ready", "params": ready})
	}
	if mode == "descendant-child" {
		spawnDescendant("descendant-grandchild", os.Stdout)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		line, _ := bufio.NewReader(conn).ReadString('\n')
		if line == "stop\n" {
			conn.Close()
			return
		}
		_, _ = io.WriteString(conn, "alive\n")
		conn.Close()
	}
}

func descendantAlive(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = io.WriteString(conn, "ping\n")
	line, _ := bufio.NewReader(conn).ReadString('\n')
	return line == "alive\n"
}

func trackDescendant(t *testing.T, data []byte) string {
	t.Helper()
	var ready struct{ Address string }
	if err := json.Unmarshal(data, &ready); err != nil || ready.Address == "" {
		t.Fatalf("invalid readiness %s: %v", data, err)
	}
	t.Cleanup(func() {
		if conn, err := net.DialTimeout("tcp", ready.Address, 100*time.Millisecond); err == nil {
			_, _ = io.WriteString(conn, "stop\n")
			conn.Close()
		}
	})
	return ready.Address
}

func assertDescendantStopped(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for descendantAlive(address) {
		if time.Now().After(deadline) {
			t.Fatalf("descendant %s survived session termination", address)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSessionTerminationCleansDescendants(t *testing.T) {
	for _, scenario := range []string{"close", "cancel", "malformed", "overflow", "exit", "detached-close", "detached-cancel", "detached-malformed", "detached-overflow", "detached-exit"} {
		t.Run(scenario, func(t *testing.T) {
			action := strings.TrimPrefix(scenario, "detached-")
			unrelated := startUnrelatedDescendant(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := fakeConfig(t, "descendants-"+action)
			if strings.HasPrefix(scenario, "detached-") {
				cfg.Env = append(cfg.Env, "PELLETS_TEST_DETACH=1")
			}
			cfg.EventBuffer = 4
			c, err := Start(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			if _, err := c.Call(context.Background(), TurnStart, nil); err != nil {
				t.Fatal(err)
			}
			var addresses []string
			for range 2 {
				e := nextEvent(t, c)
				if e.Method != "test/descendant-ready" {
					t.Fatal(e)
				}
				address := trackDescendant(t, e.Params)
				if !descendantAlive(address) {
					t.Fatal("descendant wasn't running before shutdown")
				}
				addresses = append(addresses, address)
			}
			switch action {
			case "close":
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			default:
				_, _ = c.Call(context.Background(), TurnInterrupt, nil)
			}
			err = wait(t, c)
			if action == "malformed" && !errors.Is(err, ErrProtocol) {
				t.Fatal(err)
			}
			if action == "overflow" && !errors.Is(err, ErrBufferFull) {
				t.Fatal(err)
			}
			if c.cleanupErr != nil {
				t.Fatal(c.cleanupErr)
			}
			for _, address := range addresses {
				assertDescendantStopped(t, address)
			}
			if !descendantAlive(unrelated) {
				t.Fatal("cleanup killed an unrelated detached process")
			}
		})
	}
}

func startUnrelatedDescendant(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "PELLETS_CODEX_TEST_PEER=descendant-grandchild", "GORACE=atexit_sleep_ms=0")
	detachTestDescendant(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var event Event
	if err := json.NewDecoder(stdout).Decode(&event); err != nil {
		t.Fatal(err)
	}
	address := trackDescendant(t, event.Params)
	if !descendantAlive(address) {
		t.Fatal("unrelated descendant wasn't running")
	}
	return address
}

func TestProbeTerminationCleansDescendants(t *testing.T) {
	for _, action := range []string{"exit", "cancel"} {
		t.Run(action, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ready.json")
			cfg := fakeConfig(t, "probe-descendant-"+action)
			cfg.Env = append(cfg.Env, "PELLETS_TEST_DESCENDANT_FILE="+path)
			cfg, err := normalize(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := probe(ctx, cfg, "--version"); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			var data []byte
			for {
				data, _ = os.ReadFile(path)
				if json.Valid(data) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("probe descendant readiness timed out")
				}
				time.Sleep(time.Millisecond)
			}
			address := trackDescendant(t, data)
			if action == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if action == "exit" && err != nil {
					t.Fatal(err)
				}
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("probe cleanup hung")
			}
			assertDescendantStopped(t, address)
		})
	}
}

// Frozen capability projection of the stable Codex 0.151.0 generated schemas,
// deliberately independent of requiredOperations. Full request shapes remain
// owned by Codex; fake responses exercise the transport rather than model work.
func peerSchema(dir, mode string) {
	files := map[string]map[string][]string{
		"ClientRequest.json": {
			"initialize": {"clientInfo", "capabilities"}, "thread/start": {"cwd"},
			"thread/read": {"threadId", "includeTurns"}, "thread/resume": {"threadId"},
			"turn/start": {"threadId", "input"}, "turn/steer": {"threadId", "input", "expectedTurnId"},
			"turn/interrupt": {"threadId", "turnId"}, "account/read": {"refreshToken"},
			"account/rateLimits/read": {}, "model/list": {"cursor"}, "config/read": {"includeLayers"},
			"configRequirements/read": {}, "review/start": {"threadId", "target", "delivery"},
		},
		"ClientNotification.json": {"initialized": {}},
		"ServerRequest.json": {
			"item/commandExecution/requestApproval": {}, "item/fileChange/requestApproval": {},
			"item/permissions/requestApproval": {}, "item/tool/requestUserInput": {}, "mcpServer/elicitation/request": {},
		},
		"ServerNotification.json": {"turn/completed": {"threadId", "turn"}, "serverRequest/resolved": {"requestId", "threadId"}},
	}
	if mode == "missing-method" {
		delete(files["ClientRequest.json"], "turn/steer")
	}
	if mode == "missing-field" {
		files["ClientRequest.json"]["turn/steer"] = []string{"threadId", "input"}
	}
	for file, methods := range files {
		var variants []any
		definitions := map[string]any{"BooleanSchemaExample": map[string]any{"properties": map[string]any{"disabled": false}}}
		for method, fields := range methods {
			name := strings.ReplaceAll(method, "/", "_") + "Params"
			properties := map[string]any{}
			for _, field := range fields {
				properties[field] = map[string]any{}
			}
			definitions[name] = map[string]any{"properties": properties}
			variants = append(variants, map[string]any{"properties": map[string]any{"method": map[string]any{"enum": []string{method}}, "params": map[string]any{"$ref": "#/definitions/" + name}}})
		}
		data, _ := json.Marshal(map[string]any{"oneOf": variants, "definitions": definitions})
		if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
			panic(err)
		}
	}
}

func TestRequestIDs(t *testing.T) {
	for _, bad := range []string{"null", " null ", "true", "{}", "[]", "1.5", "1e1", ""} {
		if _, err := requestKey(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	for _, good := range []string{`"string"`, `""`, "0", "-42", " 1 "} {
		if _, err := requestKey(json.RawMessage(good)); err != nil {
			t.Errorf("rejected %q: %v", good, err)
		}
	}
}

func TestUnexpectedCleanExitIsNotTurnSuccess(t *testing.T) {
	c := startFake(t, "eof", nil)
	_, _ = c.Call(context.Background(), ThreadRead, nil)
	if err := wait(t, c); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if c.LatestCompletion() != nil {
		t.Fatal("invented terminal turn")
	}
}
