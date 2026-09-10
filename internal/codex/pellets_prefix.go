package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pellets/internal/storage"
)

const pelletsPromptTemplateVersion = "pellets-codex-prefix-v1"

const (
	// These source caps leave room for worst-case JSON escaping under the
	// durable 2 MiB prefix record limit.
	maxPelletsSkillBytes        = 160 << 10
	maxPelletsVersionBytes      = 512
	maxPelletsHelpCommandBytes  = 32 << 10
	maxPelletsHelpSnapshotBytes = 128 << 10
)

var pelletsHelpCommands = [][]string{
	{"--help"},
	{"next", "--help"},
	{"start-next", "--help"},
	{"show", "--help"},
	{"list", "--help"},
	{"close", "--help"},
	{"add", "--help"},
	{"project", "--help"},
}

var pelletsPrefixCache = struct {
	sync.Mutex
	entries map[string]storage.PromptPrefix
}{entries: make(map[string]storage.PromptPrefix)}

// preparePelletsPromptPrefix snapshots only the focused Pellets skill and the
// CLI's read-only help surface. It neither writes skills/configuration nor
// injects instructions into Codex's normal AGENTS.md discovery.
func preparePelletsPromptPrefix(ctx context.Context, workspace string) (storage.PromptPrefix, error) {
	tool, err := exec.LookPath("pl")
	if err != nil {
		return storage.PromptPrefix{}, fmt.Errorf("%w: `pl` is not executable through the inherited PATH; install Pellets or add its executable directory to PATH, then retry", ErrToolUnavailable)
	}
	tool, err = filepath.Abs(tool)
	if err != nil {
		return storage.PromptPrefix{}, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(tool); resolveErr == nil {
		tool = resolved
	}
	toolDigest, err := digestFile(ctx, tool)
	if err != nil {
		return storage.PromptPrefix{}, fmt.Errorf("%w: read installed `pl` executable: %v", ErrToolUnavailable, err)
	}
	version, err := pelletsCommand(ctx, workspace, tool, maxPelletsVersionBytes, "--version")
	if err != nil {
		return storage.PromptPrefix{}, pelletsToolCommandError("`pl --version`", err)
	}
	version = normalizePromptText(version)
	if version == "" {
		return storage.PromptPrefix{}, fmt.Errorf("%w: `pl --version` returned no version", ErrToolUnavailable)
	}
	if len(strings.TrimSuffix(version, "\n")) > 512 {
		return storage.PromptPrefix{}, pelletsToolSnapshotOverflow("`pl --version` output")
	}
	skillPath, skill, err := resolvePelletsSkill(workspace)
	if err != nil {
		return storage.PromptPrefix{}, err
	}
	skill = normalizePromptText(skill)
	if skill == "" {
		return storage.PromptPrefix{}, fmt.Errorf("%w: installed Pellets skill %q is empty", ErrToolUnavailable, skillPath)
	}
	key := strings.Join([]string{tool, toolDigest, version, skillPath, digest([]byte(skill))}, "\x00")
	pelletsPrefixCache.Lock()
	cached, found := pelletsPrefixCache.entries[key]
	pelletsPrefixCache.Unlock()
	if found {
		return cached, nil
	}

	var help strings.Builder
	for _, arguments := range pelletsHelpCommands {
		output, err := pelletsCommand(ctx, workspace, tool, maxPelletsHelpCommandBytes, arguments...)
		if err != nil {
			// Do not keep a stale snapshot after a syntax or installation change.
			return storage.PromptPrefix{}, pelletsToolCommandError("`pl "+strings.Join(arguments, " ")+"`", err)
		}
		entry := "$ pl " + strings.Join(arguments, " ") + "\n" + normalizePromptText(output)
		if help.Len()+len(entry) > maxPelletsHelpSnapshotBytes {
			return storage.PromptPrefix{}, pelletsToolSnapshotOverflow("required `pl` help")
		}
		help.WriteString(entry)
		if !strings.HasSuffix(help.String(), "\n") {
			help.WriteByte('\n')
		}
	}
	helpText := help.String()
	prefix := storage.PromptPrefix{
		TemplateVersion: pelletsPromptTemplateVersion,
		SkillSHA256:     digest([]byte(skill)),
		HelpSHA256:      digest([]byte(helpText)),
		ToolExecutable:  tool,
		ToolVersion:     strings.TrimSuffix(version, "\n"),
		Text:            buildPelletsPromptPrefix(skill, helpText, tool, strings.TrimSuffix(version, "\n"), digest([]byte(skill)), digest([]byte(helpText))),
	}
	if err := storage.ValidatePromptPrefix(prefix); err != nil {
		return storage.PromptPrefix{}, err
	}
	pelletsPrefixCache.Lock()
	pelletsPrefixCache.entries[key] = prefix
	pelletsPrefixCache.Unlock()
	return prefix, nil
}

func resolvePelletsSkill(workspace string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("%w: determine the Pellets skill location: %v", ErrToolUnavailable, err)
	}
	for _, path := range []string{
		filepath.Join(workspace, ".agents", "skills", "pellets", "SKILL.md"),
		filepath.Join(home, ".agents", "skills", "pellets", "SKILL.md"),
	} {
		contents, err := readFileBounded(path, maxPelletsSkillBytes)
		if err == nil {
			absolute, absoluteErr := filepath.Abs(path)
			if absoluteErr != nil {
				return "", "", absoluteErr
			}
			return absolute, string(contents), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%w: read installed Pellets skill %q: %v", ErrToolUnavailable, path, err)
		}
	}
	return "", "", fmt.Errorf("%w: install the Pellets Codex skill with `pl skill install --scope personal --agent codex --yes`, or add it at %s", ErrToolUnavailable, filepath.Join(workspace, ".agents", "skills", "pellets", "SKILL.md"))
}

func buildPelletsPromptPrefix(skill, help, tool, version, skillSHA256, helpSHA256 string) string {
	// The sequence and LF delimiters are a cache contract. Dynamic run content
	// is appended by the scheduler only after this complete stable layer.
	return "PELLETS CODEX TASK PRELOAD v1\n" +
		"The following installed Pellets skill and CLI help were preloaded for this new conversation. They are a focused workflow layer; preserve the installed Codex system prompt, AGENTS.md instructions, tools, configuration, and any saved user instructions.\n\n" +
		"SNAPSHOT: Pellets executable " + tool + " (" + version + "); skill sha256 " + skillSHA256 + "; help sha256 " + helpSHA256 + "\n\n" +
		"INSTALLED PELLETS SKILL\n---\n" + skill + "---\n\n" +
		"REQUIRED INSTALLED PELLETS CLI HELP\n---\n" + help + "---\n\n" +
		"STABLE PELLETS WORKFLOW\nUse `pl` as the authoritative local queue and memory interface for Pellets work. Before changing work, use the documented atomic selection and preserve the current workspace's exact in-progress pellet. Do not edit `.pellets` data directly, do not create a replacement queue, and do not silently overwrite user skills or Codex configuration. Keep durable knowledge in approved project memory only when appropriate; keep independently actionable future work as focused pellets.\n\n"
}

func normalizePromptText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

func digest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func digestFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash, buffer := sha256.New(), make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if _, err := hash.Write(buffer[:count]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readFileBounded(path string, maximum int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximum {
		return nil, pelletsToolSnapshotOverflow("installed Pellets skill")
	}
	return contents, nil
}

func pelletsCommand(ctx context.Context, workspace, tool string, maximum int, arguments ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, pelletsToolProbeTimeout)
	defer cancel()
	command := exec.Command(tool, arguments...)
	command.Dir, command.Env = workspace, os.Environ()
	output := &boundedBuffer{maximum: maximum}
	command.Stdout, command.Stderr = output, io.Discard
	command.WaitDelay = time.Second
	tree, err := startOwnedProcess(commandCtx, command)
	if err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		cleanupErr := tree.terminate()
		if output.overflow {
			return "", errors.Join(pelletsToolSnapshotOverflow("`pl "+strings.Join(arguments, " ")+"` output"), cleanupErr)
		}
		return output.String(), errors.Join(err, cleanupErr)
	case <-commandCtx.Done():
		cleanupErr := tree.terminate()
		select {
		case err := <-done:
			return "", errors.Join(commandCtx.Err(), err, cleanupErr)
		case <-time.After(pelletsToolWaitTimeout):
			return "", errors.Join(commandCtx.Err(), cleanupErr, ErrCleanup)
		}
	}
}

type boundedBuffer struct {
	bytes.Buffer
	maximum  int
	overflow bool
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	available := buffer.maximum - buffer.Len()
	if available <= 0 {
		buffer.overflow = true
		return len(contents), nil
	}
	if len(contents) > available {
		buffer.overflow = true
		_, _ = buffer.Buffer.Write(contents[:available])
		return len(contents), nil
	}
	return buffer.Buffer.Write(contents)
}

func pelletsToolSnapshotOverflow(source string) error {
	return fmt.Errorf("%w: %s exceeds the bounded Pellets prompt preflight snapshot", ErrToolUnavailable, source)
}

func pelletsToolCommandError(command string, err error) error {
	return fmt.Errorf("%w: %s failed in the inherited child environment; reinstall Pellets or fix the executable, then retry: %v", ErrToolUnavailable, command, err)
}
