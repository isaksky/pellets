package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Required operations and the fields this boundary relies on. Generating the
// stable schema (without --experimental) also verifies these are available
// without opting into experimental API behavior.
var requiredOperations = map[Operation][]string{
	ThreadStart: {"cwd"}, ThreadRead: {"threadId", "includeTurns"},
	ThreadResume: {"threadId"}, TurnStart: {"threadId", "input"},
	TurnSteer:     {"threadId", "input", "expectedTurnId"},
	TurnInterrupt: {"threadId", "turnId"}, AccountRead: {"refreshToken"},
	AccountRateLimitsRead: {}, ModelList: {"cursor"}, ConfigRead: {"includeLayers"},
	ConfigRequirementsRead: {}, ReviewStart: {"threadId", "target", "delivery"},
}

var requiredInteractions = []string{
	"item/commandExecution/requestApproval", "item/fileChange/requestApproval",
	"item/permissions/requestApproval", "item/tool/requestUserInput", "mcpServer/elicitation/request",
}

func normalize(cfg Config) (Config, error) {
	if cfg.Executable == "" {
		cfg.Executable = "codex"
	}
	path, err := exec.LookPath(cfg.Executable)
	if err != nil {
		return cfg, fmt.Errorf("find Codex executable %q: install Codex or configure its path: %w", cfg.Executable, err)
	}
	cfg.Executable, err = filepath.Abs(path)
	if err != nil {
		return cfg, err
	}
	for i := 0; i < len(cfg.Arguments); i += 2 {
		if i+1 == len(cfg.Arguments) {
			return cfg, fmt.Errorf("Codex argument %q needs a value", cfg.Arguments[i])
		}
		switch cfg.Arguments[i] {
		case "-c", "--config", "--enable", "--disable":
		default:
			return cfg, fmt.Errorf("unsupported Codex global argument %q; use configuration/feature pairs; transport is always stdio", cfg.Arguments[i])
		}
	}
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = "dev"
	}
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = 8 << 20
	}
	if cfg.EventBuffer == 0 {
		cfg.EventBuffer = 256
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = 128
	}
	if cfg.StderrBytes == 0 {
		cfg.StderrBytes = 32 << 10
	}
	if cfg.MaxMessageBytes < 256 || cfg.EventBuffer < 1 || cfg.MaxPending < 1 || cfg.StderrBytes < 1 {
		return cfg, fmt.Errorf("Codex limits must be positive; MaxMessageBytes must be at least 256")
	}
	cfg.Arguments = append([]string(nil), cfg.Arguments...)
	cfg.Env = append([]string(nil), cfg.Env...)
	return cfg, nil
}

func command(cfg Config, args ...string) *exec.Cmd {
	cmd := exec.Command(cfg.Executable, append(append([]string(nil), cfg.Arguments...), args...)...)
	cmd.Dir = cfg.Dir
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.WaitDelay = time.Second
	return cmd
}

func inspect(ctx context.Context, cfg Config) (Runtime, error) {
	info := Runtime{Executable: cfg.Executable}
	version, err := probe(ctx, cfg, "--version")
	if err != nil {
		return info, err
	}
	info.Version = strings.TrimSpace(version)
	if !regexp.MustCompile(`^codex-cli [0-9]+\.[0-9]+\.[0-9]+(?:[-+][^\s]+)?$`).MatchString(info.Version) {
		return info, fmt.Errorf("%w: unexpected version output from %s", ErrUnsupported, cfg.Executable)
	}
	dir, err := os.MkdirTemp("", "pellets-codex-schema-")
	if err != nil {
		return info, err
	}
	defer os.RemoveAll(dir)
	if _, err = probe(ctx, cfg, "app-server", "generate-json-schema", "--out", dir); err != nil {
		return info, fmt.Errorf("%w: %s cannot generate its stable schema: %w", ErrUnsupported, info.Version, err)
	}
	checks := map[string]map[string][]string{
		"ClientRequest.json":      {"initialize": {"clientInfo"}},
		"ClientNotification.json": {"initialized": nil},
		"ServerNotification.json": {"turn/completed": {"threadId", "turn"}, "serverRequest/resolved": {"requestId"}},
		"ServerRequest.json":      {},
	}
	for op, fields := range requiredOperations {
		checks["ClientRequest.json"][string(op)] = fields
	}
	for _, method := range requiredInteractions {
		checks["ServerRequest.json"][method] = nil
	}
	for file, methods := range checks {
		if err := checkSchema(filepath.Join(dir, file), methods); err != nil {
			return info, fmt.Errorf("%w: %s (%s): %v", ErrUnsupported, info.Version, cfg.Executable, err)
		}
	}
	return info, nil
}

func probe(ctx context.Context, cfg Config, args ...string) (output string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := command(cfg, args...)
	out := &tailBuffer{limit: 64 << 10}
	stderr := &tailBuffer{limit: cfg.StderrBytes}
	cmd.Stdout, cmd.Stderr = out, stderr
	tree, err := startProcessTree(cmd)
	if err != nil {
		return "", fmt.Errorf("start Codex %s: %w", args[0], err)
	}
	defer func() { err = errors.Join(err, tree.terminate()) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("Codex %s failed: %w (stderr: %s)", args[0], err, stderr.String())
		}
		return out.String(), nil
	case <-ctx.Done():
		if tree.terminate() != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return "", fmt.Errorf("Codex %s: %w", args[0], ctx.Err())
	}
}

type schemaNode struct {
	Ref         string                `json:"$ref"`
	Enum        []string              `json:"enum"`
	Properties  map[string]schemaNode `json:"properties"`
	Definitions map[string]schemaNode `json:"definitions"`
	OneOf       []schemaNode          `json:"oneOf"`
}

func (n *schemaNode) UnmarshalJSON(data []byte) error {
	// JSON Schema permits boolean schemas in addition to object schemas. They
	// occur in unrelated generated definitions and carry no method/field shape.
	if string(data) == "true" || string(data) == "false" {
		*n = schemaNode{}
		return nil
	}
	type object schemaNode
	return json.Unmarshal(data, (*object)(n))
}

func checkSchema(path string, required map[string][]string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 16<<20 {
		return fmt.Errorf("schema %s exceeds 16 MiB", filepath.Base(path))
	}
	var root schemaNode
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	found := map[string]schemaNode{}
	for _, variant := range root.OneOf {
		for _, method := range variant.Properties["method"].Enum {
			found[method] = variant.Properties["params"]
		}
	}
	for method, fields := range required {
		params, ok := found[method]
		if !ok {
			return fmt.Errorf("%s is missing method %s", filepath.Base(path), method)
		}
		if params.Ref != "" {
			params = root.Definitions[strings.TrimPrefix(params.Ref, "#/definitions/")]
		}
		for _, field := range fields {
			if _, ok := params.Properties[field]; !ok {
				return fmt.Errorf("%s is missing parameter %s", method, field)
			}
		}
	}
	return nil
}

// tailBuffer drains stderr continuously while retaining only a bounded tail.
type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return n, nil
	}
	if extra := len(b.data) + len(p) - b.limit; extra > 0 {
		copy(b.data, b.data[extra:])
		b.data = b.data[:len(b.data)-extra]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
