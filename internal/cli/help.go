package cli

import (
	"fmt"
	"regexp"
	"strings"

	"pellets/internal/domain"
)

func isHelpFlag(arg string) bool { return arg == "--help" || arg == "-h" }

// Wrap syntax at word boundaries without changing literal examples or prose in
// the longer family help pages.
func wrapUsage(usage string) string {
	lines := strings.Split(usage, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			break
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "pl ") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		column := indent + 2
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", indent))
		for j, word := range strings.Fields(line) {
			if j > 0 {
				if column+1+len(word) > 88 {
					b.WriteString("\n    ")
					column = 4
				} else {
					b.WriteByte(' ')
					column++
				}
			}
			b.WriteString(word)
			column += len(word)
		}
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}

var helpFlagPattern = regexp.MustCompile(`--[a-z][a-z-]*`)

// Shared descriptions are selected from each command's declared syntax. Keep
// command-specific semantics (such as list vs. search defaults) in the examples.
var optionHelp = map[string]string{
	"--request-id":                   "ID: reuse the same ID when retrying the same add operation.",
	"--description":                  "TEXT: set the description; quote text containing spaces.",
	"--description-file":             "PATH: read the description from a UTF-8 file; - reads stdin.",
	"--external-id":                  "ID: set or filter by an exact external reference.",
	"--group":                        "GROUP: set or filter by an exact group name.",
	"--before":                       "PELLET: place this pellet immediately before another active pellet.",
	"--after":                        "PELLET: place this pellet immediately after another active pellet.",
	"--maybe-later":                  "Create deferred work outside the active queue.",
	"--review-targets":               "PELLET,PELLET: create a review checkpoint for the selected pellets.",
	"--status":                       "STATUS: open, in_progress, closed, or maybe_later.",
	"--all":                          "Include all statuses instead of only the active queue.",
	"--limit":                        "N: return at most this positive number of records.",
	"--project":                      "CODE: explicitly select the project.",
	"--closed-before":                "DATE: select pellets completed before YYYY-MM-DD or an RFC 3339 time.",
	"--dry-run":                      "Preview the action without making changes.",
	"--yes":                          "Explicitly approve the action without an interactive confirmation.",
	"--title":                        "TEXT: replace the title.",
	"--clear-external-id":            "Remove the external reference.",
	"--clear-group":                  "Remove group membership.",
	"--recover-workspace":            "WORKSPACE_ID: recover work from an unavailable workspace.",
	"--text":                         "TEXT: supply memory text directly.",
	"--file":                         "PATH: read memory text from a UTF-8 file; - reads stdin.",
	"--created-by":                   "agent|human: record the author's provenance (default: agent).",
	"--approved-only":                "Return only human-approved memory.",
	"--delete-conflicting-redirects": "Allow deletion of the displayed rename conflicts; requires --yes.",
	"--scope":                        "repo|personal: install in this repository or your home directory.",
	"--agent":                        "codex|claude|both: choose which agent receives the skill.",
	"--force":                        "Allow replacement of different existing skill content after approval.",
	"--port":                         "PORT: use this exact port; 0 asks the OS for an available port.",
	"--no-open":                      "Start the server without opening a browser.",
}

var commandExamples = map[string]string{
	"add":        "  pl add \"Fix a bug\"\n  pl add \"Improve the CLI\" --description-file details.md\n  pl add \"Improve the CLI\" --description-file - < details.md",
	"list":       "  pl list\n  pl list --status closed --limit 10\n  pl --json list\n\nWithout a status filter, list shows the active queue.",
	"search":     "  pl search \"CLI\"\n  pl search \"CLI\" --status open\n\nSearch includes all statuses unless --status is supplied.",
	"show":       "  pl show foo-123\n\nReplace foo-123 with a full reference from pl list (not a bare number).",
	"edit":       "  pl edit foo-123 --title \"Clarify CLI help\"\n  pl edit foo-123 --description-file details.md",
	"move":       "  pl move foo-123 --before foo-124",
	"next":       "  pl next\n  pl next --group cli\n\nThis only reads the queue; use pl start-next to claim work.",
	"start":      "  pl start foo-123",
	"start-next": "  pl start-next\n  pl start-next --group cli",
	"close":      "  pl close foo-123",
	"release":    "  pl release foo-123",
	"defer":      "  pl defer foo-123",
	"reopen":     "  pl reopen foo-123",
	"purge":      "  pl purge --project foo --dry-run\n  pl purge --project foo\n\nPurge permanently deletes closed pellets. Review the preview first.\nIn a terminal, deletion prompts for confirmation; automation requires --yes.",
	"project":    "  pl project list\n  pl project show\n  pl --project foo project rename bar",
	"memory":     "  pl memory add --text \"Identifiers preserve underscores.\" --created-by agent\n  pl memory search \"identifiers\" --approved-only\n  pl memory show 1\n\nMemory stores durable project knowledge. Use pl add for future work.",
	"skill":      "  pl skill install\n  pl skill install --scope personal --agent codex --dry-run\n\nIn a terminal, missing choices and replacement decisions are prompted.",
	"server":     "  pl server\n  pl server --port 7419 --no-open\n\nThe server runs in the foreground. Press Ctrl-C to stop it.",
	"init-db":    "  pl init-db\n\nUsually unnecessary: the first project command discovers or creates a database.\nUse this to deliberately create a database in the current directory.",
}

func writeCommandDetails(b *strings.Builder, command Command) {
	// Group help already contains full option explanations and literal Markdown
	// examples. Preserve those instead of producing duplicate sections.
	if command.Name == "group" {
		return
	}
	seen := make(map[string]bool)
	for _, flag := range helpFlagPattern.FindAllString(command.Usage, -1) {
		if seen[flag] || optionHelp[flag] == "" {
			continue
		}
		if len(seen) == 0 {
			b.WriteString("\nOptions:\n")
		}
		seen[flag] = true
		fmt.Fprintf(b, "  %s\n    %s\n", flag, optionHelp[flag])
	}
	if examples := commandExamples[command.Name]; examples != "" {
		fmt.Fprintf(b, "\nExamples:\n%s\n", examples)
	}
}

func usageHint(err error, command Command) string {
	public := domain.PublicError(err)
	if public.Kind != domain.Usage {
		return ""
	}
	if flag := misplacedGlobalFlag(err, command); flag != "" {
		// Do not rewrite arbitrary payloads into executable suggestions. The
		// simple example explains placement without discarding real arguments.
		value := ""
		if flag == "--project" {
			value = " CODE"
		}
		return fmt.Sprintf("For example:\n  pl %s%s %s\nRun 'pl %s --help' for command options and examples.\n", flag, value, command.Name, command.Name)
	}
	if command.Name != "" {
		syntax, _, _ := strings.Cut(command.Usage, "\n\n")
		if syntax == "" {
			syntax = "pl " + command.Name
		}
		hint := "\nUsage:\n  " + wrapUsage(syntax) + "\n"
		if examples := commandExamples[command.Name]; examples != "" {
			first, _, _ := strings.Cut(examples, "\n")
			hint += "\nExample:\n" + first + "\n"
		}
		return hint + fmt.Sprintf("\nRun 'pl %s --help' for options and examples.\n", command.Name)
	}
	return "Run 'pl --help' for available commands and global options.\n"
}

func misplacedGlobalFlag(err error, command Command) string {
	public := domain.PublicError(err)
	if public.Code == "unknown_flag" && command.Name != "" {
		flag, _ := public.Details["flag"].(string)
		switch flag {
		case "--json", "--pretty", "--human", "--project", "--version":
			return flag
		}
	}
	return ""
}
