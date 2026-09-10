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
	ErrCleanup     = errors.New("owned Codex process cleanup could not be confirmed")
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

// CachedInputTokens extracts optional token-cache telemetry from a terminal or
// token-usage app-server notification. Both documented camelCase and
// snake_case response forms are accepted because this adapter otherwise
// preserves runtime JSON.
func CachedInputTokens(event *Event) (int64, bool) {
	if event == nil || (event.Method != "turn/completed" && event.Method != "thread/tokenUsage/updated") {
		return 0, false
	}
	var payload any
	if json.Unmarshal(event.Params, &payload) != nil {
		return 0, false
	}
	return cachedInputTokens(payload)
}

// CachedInputTokensForTurn additionally binds telemetry to the exact recorded
// conversation identity. The runtime emits token usage independently from a
// terminal turn notification, so callers retain it until completion.
func CachedInputTokensForTurn(event *Event, threadID, turnID string) (int64, bool) {
	if event == nil || threadID == "" || turnID == "" {
		return 0, false
	}
	var identity struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(event.Params, &identity) != nil || identity.ThreadID != threadID {
		return 0, false
	}
	if identity.TurnID == "" {
		identity.TurnID = identity.Turn.ID
	}
	if identity.TurnID != turnID {
		return 0, false
	}
	return CachedInputTokens(event)
}

func cachedInputTokens(value any) (int64, bool) {
	switch value := value.(type) {
	case map[string]any:
		for _, name := range []string{"cachedInputTokens", "cached_input_tokens"} {
			if number, ok := value[name].(float64); ok && number >= 0 && number == float64(int64(number)) {
				return int64(number), true
			}
		}
		// ThreadTokenUsage contains both last and total summaries. The scheduler
		// needs this turn's usage, so prefer last explicitly rather than a
		// cumulative thread total or Go map iteration order.
		for _, name := range []string{"tokenUsage", "last", "total", "usage", "turn"} {
			if child, exists := value[name]; exists {
				if number, ok := cachedInputTokens(child); ok {
					return number, true
				}
			}
		}
	case []any:
		for _, child := range value {
			if number, ok := cachedInputTokens(child); ok {
				return number, true
			}
		}
	}
	return 0, false
}
