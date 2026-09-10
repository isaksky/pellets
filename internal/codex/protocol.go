// Package codex adapts the installed Codex app-server's stdio protocol. It does
// not own queue policy, approval decisions, credentials, or a model/tool loop.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Operation is a supported client request. Params and results retain the
// runtime's JSON shape, including optional fields unknown to this adapter.
type Operation string

const (
	ThreadStart            Operation = "thread/start"
	ThreadRead             Operation = "thread/read"
	ThreadResume           Operation = "thread/resume"
	TurnStart              Operation = "turn/start"
	TurnSteer              Operation = "turn/steer"
	TurnInterrupt          Operation = "turn/interrupt"
	AccountRead            Operation = "account/read"
	AccountRateLimitsRead  Operation = "account/rateLimits/read"
	ModelList              Operation = "model/list"
	ConfigRead             Operation = "config/read"
	ConfigRequirementsRead Operation = "configRequirements/read"
	ReviewStart            Operation = "review/start"
)

// Session is the runner-facing boundary. Drain Events throughout the session,
// including during Call. Calls may run concurrently; events preserve wire order.
// Respond is explicit: the adapter never automatically approves server requests.
type Session interface {
	Call(context.Context, Operation, any) (json.RawMessage, error)
	Respond(context.Context, json.RawMessage, any, *RPCError) error
	Events() <-chan Event
	LatestCompletion() *Event
	Wait(context.Context) error
	Close() error
}

// Event is either a notification (ID absent), or a server request requiring a
// response with the exact ID. Unknown notifications are delivered unchanged.
type Event struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	ID     json.RawMessage `json:"id,omitempty"`
}

// RPCError preserves the runtime's structured error, including optional data.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("codex RPC %d: %s", e.Code, e.Message) }

var (
	ErrClosed      = errors.New("codex session closed")
	ErrBufferFull  = errors.New("codex buffer limit reached; drain events promptly or increase the configured limit")
	ErrProtocol    = errors.New("invalid Codex app-server protocol message")
	ErrUnsupported = errors.New("unsupported Codex app-server capability; update the configured Codex executable")
)

// Config selects a local executable. Arguments are optional Codex global
// --config/-c, --enable, or --disable pairs, never a shell command or transport.
// Env is appended to the inherited environment; nil reuses installed settings.
// Zero limits use defaults. The Start context owns the complete session lifetime.
type Config struct {
	Executable      string
	Arguments       []string
	Dir             string
	Env             []string
	ClientVersion   string
	MaxMessageBytes int
	EventBuffer     int
	MaxPending      int
	StderrBytes     int
}

// Runtime records what was inspected, without copying credentials or config.
type Runtime struct {
	Executable string
	Version    string
	UserAgent  string
}
