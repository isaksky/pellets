package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/output"
)

func (g GlobalOptions) machine() bool { return g.JSON || g.Pretty }

// Even a malformed invocation must honor explicit machine output. Only examine
// global options, never payloads or command arguments that happen to say --json.
func outputOptions(args []string) GlobalOptions {
	var g GlobalOptions
	for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		name, _, value := splitOption(args[i])
		switch name {
		case "--json":
			g.JSON = true
		case "--pretty":
			g.Pretty = true
		case "--human":
			g.Human = true
		case "--project":
			if !value && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		}
	}
	return g
}

// Reject unavailable decisions before discovery/bootstrap and without touching
// stdin. Payload readers remain exclusively owned by their explicit options.
func validateInteraction(inv Invocation) error {
	if inv.Interactive {
		return nil
	}
	switch input := inv.Input.(type) {
	case purgeInput:
		if !input.DryRun && !input.Yes {
			return domain.NewError(domain.Confirmation, "confirmation_required", "purge requires exactly one of --dry-run or --yes", map[string]any{"flags": []string{"--dry-run", "--yes"}})
		}
	case lifecycleInput:
		if input.RecoveryWorkspaceID != nil && !input.Yes {
			return domain.NewError(domain.Confirmation, "confirmation_required", "workspace recovery requires --yes", map[string]any{"workspace_id": *input.RecoveryWorkspaceID})
		}
	case memoryInput:
		if input.Action == "remove" && !input.Yes {
			return domain.NewError(domain.Confirmation, "confirmation_required", "memory removal requires --yes", map[string]any{"memory_id": input.ID})
		}
	}
	return nil
}

type interaction struct {
	reader *bufio.Reader
	writer io.Writer
}

func newInteraction(reader io.Reader, writer io.Writer) *interaction {
	return &interaction{reader: bufio.NewReader(reader), writer: writer}
}

func (p *interaction) confirm(prompt string) (bool, error) {
	if _, err := io.WriteString(p.writer, prompt); err != nil {
		return false, err
	}
	for {
		answer, err := p.readAnswer()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "y", "yes":
			return true, nil
		case "", "n", "no", "0", "cancel", "c", "q", "quit":
			return false, nil
		default:
			if _, err := io.WriteString(p.writer, "Enter yes to continue or no to cancel: "); err != nil {
				return false, err
			}
		}
	}
}

func (p *interaction) readAnswer() (string, error) {
	// Catch interruption only while collecting a decision. No mutation can begin
	// until a complete answer is received; partial input followed by EOF cancels.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() { line, err := p.reader.ReadString('\n'); answers <- answer{line, err} }()
	select {
	case <-interrupts:
		return "", nil
	case result := <-answers:
		// If an answer and interruption arrived together, cancellation wins.
		select {
		case <-interrupts:
			return "", nil
		default:
		}
		if result.err == io.EOF || strings.ContainsAny(result.line, "\x03\x04") {
			return "", nil
		}
		if result.err != nil {
			return "", result.err
		}
		return strings.TrimSpace(result.line), nil
	}
}

type cancelledData struct{}

func (cancelledData) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "Cancelled; no changes were made.\n")
	return err
}

func automationHint(err error, args []string) string {
	code := domain.PublicError(err).Code
	extra := []string{}
	switch code {
	case "confirmation_required":
		extra = append(extra, "--yes")
	case "missing_skill_choices":
		return "Prompts require terminal stdin and stdout. For automation, specify choices, e.g.: pl --json skill install --scope personal --agent codex --yes"
	case "skill_content_conflict":
		extra = append(extra, "--force", "--yes")
	default:
		return ""
	}
	retry := []string{"pl", "--json"}
	for _, arg := range args {
		if arg != "--human" && arg != "--json" && arg != "--pretty" {
			retry = append(retry, arg)
		}
	}
	for _, flag := range extra {
		found := false
		for _, arg := range retry {
			if arg == flag {
				found = true
			}
		}
		if !found {
			retry = append(retry, flag)
		}
	}
	return fmt.Sprintf("Prompts require terminal stdin and stdout. After reviewing the action, use: %s", output.ShellCommand(retry))
}
