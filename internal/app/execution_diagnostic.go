package app

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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
	text := diagnosticANSI.ReplaceAllString(strings.ToValidUTF8(stderr, "�"), "")
	text = diagnosticSecret.ReplaceAllString(text, "[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		text = "Git command failed; inspect the repository and installed Git tools."
	} else {
		text = "Git: " + text
	}
	if len(text) > storage.MaxRunSummaryBytes {
		text = text[:storage.MaxRunSummaryBytes-3]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "..."
	}
	return &executionGitFailure{cause: cause, diagnostic: text}
}
func executionFailureDiagnostic(err error) string {
	var failure *executionGitFailure
	if errors.As(err, &failure) {
		return failure.diagnostic
	}
	return ""
}
