package app

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

type executionGitFailure struct {
	cause      error
	diagnostic string
}

func (e *executionGitFailure) Error() string { return e.diagnostic }
func (e *executionGitFailure) Unwrap() error { return e.cause }

var diagnosticANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

// A quote starts a potentially spaced or malformed credential. Conservatively
// redact the rest of that line rather than falling back to a single word when
// the closing quote is absent or escaped. Later diagnostic lines are retained.
var diagnosticSecret = regexp.MustCompile(`(?i)(?:authorization["']?\s*:\s*(?:["'][^\r\n]*|\S+\s+(?:["'][^\r\n]*|\S+))|bearer\s+(?:["'][^\r\n]*|\S+)|(?:password|passwd|token|secret|api[_-]?key)["']?\s*[:=]\s*(?:["'][^\r\n]*|[^\s,;]+)|sk-[A-Za-z0-9_-]{8,}|https?://[^\s/@]+:[^\s/@]+@[^\s]+)`)

func newExecutionGitFailure(cause error, stderr string) error {
	return &executionGitFailure{cause: cause, diagnostic: sanitizeExecutionDiagnostic("Git", stderr)}
}

func sanitizeExecutionDiagnostic(source, stderr string) string {
	text := diagnosticANSI.ReplaceAllString(strings.ToValidUTF8(stderr, "�"), "")
	text = diagnosticSecret.ReplaceAllString(text, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		text = source + " operation failed; inspect the selected runtime and workspace."
		if source == "Git" {
			text = "Git command failed; inspect the repository and installed Git tools."
		}
	} else {
		text = source + ": " + text
	}
	if len(text) > storage.MaxRunSummaryBytes {
		text = text[:storage.MaxRunSummaryBytes-3]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "..."
	}
	return text
}
func executionFailureDiagnostic(err error) string {
	var failure *executionGitFailure
	if errors.As(err, &failure) {
		return failure.diagnostic
	}
	var rpc *codex.RPCError
	if errors.As(err, &rpc) {
		return sanitizeExecutionDiagnostic("Codex", rpc.Message)
	}
	var public *domain.Error
	if errors.As(err, &public) && (public.Code == "codex_turn_unsuccessful" || public.Code == "codex_review_unsuccessful" || public.Code == "triage_result_invalid") {
		return public.Message
	}
	return ""
}

// Only the documented error message is allowed out, never RPC data, additional
// details, raw notifications, stderr, or transcript text.
func codexTurnDiagnostic(event *codex.Event, threadID, turnID string) string {
	if completedTurnStatus(event, threadID, turnID) != "failed" {
		return ""
	}
	var payload struct {
		Turn struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(event.Params, &payload) != nil || payload.Turn.Error == nil || payload.Turn.Error.Message == "" {
		return ""
	}
	return sanitizeExecutionDiagnostic("Codex", payload.Turn.Error.Message)
}

func codexPreflightFailure(err error) error {
	if err == nil {
		return nil
	}
	return domain.WrapError(domain.Conflict, "codex_preflight_failed", sanitizeExecutionDiagnostic("Codex preflight", err.Error()), nil, err)
}
