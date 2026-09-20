package storage

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"pellets/internal/domain"
)

const (
	FinalizationMessageVersion = 1
	MaxCommitSubjectBytes      = 240
	MaxCommitBodyBytes         = 16 << 10
	MaxCommitMessageBytes      = MaxCommitSubjectBytes + MaxCommitBodyBytes + 512
)

var pelletSubjectPrefix = regexp.MustCompile(`(?i)^[a-z][a-z0-9_-]*-[0-9]+\s*:`)

// BuildFinalizationMessage defines version 1's canonical bytes. The subject
// must already be a single trimmed line. Body CRLF becomes LF; outer blank
// lines are removed, but interior whitespace and literal text are preserved.
// Only the server supplies the reference. Git must use --cleanup=verbatim.
// Errors deliberately never echo proposed content into execution summaries.
func BuildFinalizationMessage(subject, body, reference string) (string, error) {
	if subject == "" || len(subject) > MaxCommitSubjectBytes || !utf8.ValidString(subject) || strings.TrimSpace(subject) != subject || strings.IndexFunc(subject, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	}) >= 0 {
		return "", fmt.Errorf("commit_subject must be a nonempty trimmed UTF-8 line of at most %d bytes, without control characters", MaxCommitSubjectBytes)
	}
	plain := strings.ToLower(strings.Trim(subject, " .:!"))
	// Recognize generic placeholders even with a conventional-commit prefix.
	if _, suffix, ok := strings.Cut(plain, ": "); ok {
		plain = suffix
	}
	plain = strings.Join(strings.Fields(plain), " ")
	if strings.IndexFunc(subject, unicode.IsLetter) < 0 {
		return "", fmt.Errorf("commit_subject must describe the delivered change, not punctuation or a numeric placeholder")
	}
	switch plain {
	case "implement pellet", "implement the pellet", "implement task", "implement the task", "complete pellet", "complete task", "update files", "update code", "make changes", "fix bug", "fix bugs", "changes", "done", "wip", "todo", "tbd", "n/a", "placeholder", "commit subject", "commit message":
		return "", fmt.Errorf("commit_subject is a placeholder; describe the specific delivered change and follow repository conventions")
	}
	if pelletSubjectPrefix.MatchString(subject) {
		return "", fmt.Errorf("commit_subject must lead with the delivered change; the server adds Pellet traceability as a trailer")
	}
	if len(body) > MaxCommitBodyBytes || !utf8.ValidString(body) {
		return "", fmt.Errorf("commit_body must be UTF-8 text of at most %d bytes", MaxCommitBodyBytes)
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if strings.IndexFunc(body, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return "", fmt.Errorf("commit_body may contain line breaks and tabs but no other control characters")
	}
	for _, line := range strings.Split(body, "\n") {
		key, _, hasSeparator := strings.Cut(line, ":")
		if hasSeparator && strings.EqualFold(strings.TrimSpace(key), "pellet") {
			return "", fmt.Errorf("omit Pellet trailers from commit_body; the server supplies the bound identity")
		}
	}
	body = strings.Trim(body, "\n")
	if strings.TrimSpace(body) == "" {
		body = ""
	}
	if _, err := domain.ParsePelletReference(reference); err != nil {
		return "", fmt.Errorf("invalid server-bound commit reference")
	}
	message := subject + "\n\n"
	if body != "" {
		message += body + "\n\n"
	}
	message += "Pellet: " + reference + "\n"
	if len(message) > MaxCommitMessageBytes {
		return "", fmt.Errorf("commit message exceeds its storage bound")
	}
	return message, nil
}

func validateFinalizationMessage(f *FinalizationEvidence) error {
	if f.MessageVersion == 0 {
		// Preserve the exact pre-versioned subject-only receipt contract. Never
		// apply new editorial requirements to historical recovery evidence.
		if f.Message != "" || len(f.Subject) == 0 || len(f.Subject) > 240 || strings.ContainsAny(f.Subject, "\x00\r\n") {
			return InvalidExecutionRun("invalid legacy finalization subject")
		}
		return nil
	}
	if f.MessageVersion != FinalizationMessageVersion {
		return InvalidExecutionRun("unsupported finalization message version")
	}
	if f.NoChanges {
		if f.Subject != "" || f.Message != "" {
			return InvalidExecutionRun("already-satisfied evidence must not contain a commit message")
		}
		return nil
	}
	if len(f.Message) > MaxCommitMessageBytes || !strings.HasPrefix(f.Message, f.Subject+"\n\n") || !strings.HasSuffix(f.Message, "\n") {
		return InvalidExecutionRun("invalid finalization message")
	}
	trailer := strings.LastIndex(f.Message, "\n\nPellet: ")
	if trailer < len(f.Subject) || trailer == len(f.Subject)+1 {
		return InvalidExecutionRun("missing bound finalization trailer")
	}
	body := ""
	if trailer > len(f.Subject) {
		body = f.Message[len(f.Subject)+2 : trailer]
	}
	reference := strings.TrimSuffix(f.Message[trailer+len("\n\nPellet: "):], "\n")
	expected, err := BuildFinalizationMessage(f.Subject, body, reference)
	if err != nil || expected != f.Message {
		return InvalidExecutionRun("finalization message is not canonical version 1 text")
	}
	return nil
}
