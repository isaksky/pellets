package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"pellets/internal/app"
	"pellets/internal/domain"
)

func TestSkillInstallJSONIsNonInteractiveAndDatabaseIndependent(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	home := filepath.Join(temporary, "home 界")
	repository := filepath.Join(temporary, "repository 界")
	working := filepath.Join(repository, "nested path")
	for _, path := range []string{home, working} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A malformed database proves this command skips database discovery/open.
	if err := os.Mkdir(filepath.Join(repository, ".pellets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".pellets", "pellets.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}

	application := newSkillTestApp(t, home, repository, working, errorReader{}, true)
	var stdout, stderr bytes.Buffer
	exit := application.Run([]string{"skill", "install", "--scope", "repo", "--agent", "both", "--yes"}, &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("skill install = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
	var envelope struct {
		SchemaVersion int              `json:"schema_version"`
		Command       string           `json:"command"`
		Data          skillInstallData `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != "skill install" || envelope.Data.Status != "installed" || envelope.Data.Scope != app.SkillScopeRepository || envelope.Data.Agent != app.SkillAgentBoth || envelope.Data.RepositoryRoot != repository || len(envelope.Data.Targets) != 2 {
		t.Fatalf("JSON result = %#v", envelope)
	}
	for _, target := range envelope.Data.Targets {
		got, err := os.ReadFile(target.Path)
		if err != nil || string(got) != app.PelletsSkillContent() || target.Result != "installed" {
			t.Errorf("target = %#v, content %q, error %v", target, got, err)
		}
	}
	stdout.Reset()
	stderr.Reset()
	exit = application.Run([]string{"skill", "install", "--scope", "repo", "--agent", "both", "--yes"}, &stdout, &stderr)
	if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"status":"idempotent"`) || strings.Count(stdout.String(), `"result":"idempotent"`) != 2 {
		t.Fatalf("idempotent JSON = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
	if got, err := os.ReadFile(filepath.Join(repository, ".pellets", "pellets.db")); err != nil || string(got) != "not sqlite" {
		t.Fatalf("skill install touched database: %q, %v", got, err)
	}
	for _, path := range []string{".gitignore", "AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(repository, path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("skill install created protected path %q: %v", path, err)
		}
	}
}

func TestSkillInstallRequiresChoicesAndConfirmationWithoutPrompts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	tests := []struct {
		name string
		args []string
		code string
		exit int
	}{
		{"missing choices", []string{"skill", "install"}, "missing_skill_choices", 2},
		{"missing agent", []string{"skill", "install", "--scope", "personal"}, "missing_skill_choices", 2},
		{"confirmation", []string{"skill", "install", "--scope", "personal", "--agent", "codex"}, "confirmation_required", 6},
		{"invalid scope", []string{"skill", "install", "--scope", "workspace", "--agent", "codex", "--yes"}, "invalid_skill_scope", 2},
		{"invalid agent", []string{"skill", "install", "--scope", "personal", "--agent", "cursor", "--yes"}, "invalid_skill_agent", 2},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			application := newSkillTestApp(t, root, "", root, errorReader{}, true)
			var stdout, stderr bytes.Buffer
			exit := application.Run(test.args, &stdout, &stderr)
			if exit != test.exit || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("result = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(root, ".agents")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed validation wrote filesystem state: %v", err)
			}
		})
	}
}

func TestSkillInstallInteractiveRepositoryWizardAndYesBehavior(t *testing.T) {
	t.Parallel()

	t.Run("full wizard", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "home")
		repository := filepath.Join(temporary, "repository with spaces 界")
		working := filepath.Join(repository, "nested")
		for _, path := range []string{home, working} {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		application := newSkillTestApp(t, home, repository, working, strings.NewReader("1\n3\ny\n"), true)
		var stdout, stderr bytes.Buffer
		exit := application.Run([]string{"--human", "skill", "install"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 {
			t.Fatalf("wizard = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
		for _, required := range []string{
			"Git repository root: " + repository,
			"1) Repository", "2) Personal", "1) Codex", "2) Claude", "3) Both",
			filepath.Join(repository, ".agents", "skills", "pellets", "SKILL.md"),
			filepath.Join(repository, ".claude", "skills", "pellets", "SKILL.md"),
			"Install the Pellets skill at every displayed destination?",
		} {
			if !strings.Contains(stdout.String(), required) {
				t.Errorf("wizard output does not contain %q:\n%s", required, stdout.String())
			}
		}
		for _, relative := range []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"} {
			got, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(relative)))
			if err != nil || string(got) != app.PelletsSkillContent() {
				t.Errorf("installed %q = %q, %v", relative, got, err)
			}
		}
	})

	t.Run("yes suppresses only final confirmation", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "home")
		repository := filepath.Join(temporary, "repository")
		for _, path := range []string{home, repository} {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		application := newSkillTestApp(t, home, repository, repository, strings.NewReader("1\n1\n"), true)
		var stdout, stderr bytes.Buffer
		exit := application.Run([]string{"--human", "skill", "install", "--yes"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Choose installation scope") || !strings.Contains(stdout.String(), "Choose agent target") || strings.Contains(stdout.String(), "Install the Pellets skill at every displayed destination?") {
			t.Fatalf("--yes wizard = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
	})
}

func TestSkillInstallInteractiveOutsideGitAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("outside Git offers personal only", func(t *testing.T) {
		home := t.TempDir()
		working := t.TempDir()
		application := newSkillTestApp(t, home, "", working, strings.NewReader("3\ny\n"), true)
		var stdout, stderr bytes.Buffer
		exit := application.Run([]string{"--human", "skill", "install"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Repository scope is unavailable") || strings.Contains(stdout.String(), "Choose installation scope") {
			t.Fatalf("outside-Git wizard = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
		for _, relative := range []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"} {
			if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(relative))); err != nil {
				t.Errorf("personal target %q: %v", relative, err)
			}
		}
	})

	for _, test := range []struct {
		name   string
		input  string
		args   []string
		marker string
	}{
		{"scope", "0\n", []string{"--human", "skill", "install"}, "Choose installation scope"},
		{"agent", "1\n0\n", []string{"--human", "skill", "install"}, "Choose agent target"},
		{"final", "1\n1\nn\n", []string{"--human", "skill", "install"}, "Install the Pellets skill"},
	} {
		test := test
		t.Run("cancel at "+test.name, func(t *testing.T) {
			temporary := t.TempDir()
			home := filepath.Join(temporary, "home")
			repository := filepath.Join(temporary, "repository")
			for _, path := range []string{home, repository} {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			application := newSkillTestApp(t, home, repository, repository, strings.NewReader(test.input), true)
			var stdout, stderr bytes.Buffer
			exit := application.Run(test.args, &stdout, &stderr)
			if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), test.marker) || !strings.Contains(stdout.String(), "Installation cancelled; no files were written.") {
				t.Fatalf("cancel = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
			}
			for _, root := range []string{home, repository} {
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("cancellation left entries in %q: %v, %v", root, entries, err)
				}
			}
		})
	}
}

func TestSkillInstallConflictDryRunAndInteractiveReplacement(t *testing.T) {
	t.Parallel()

	t.Run("dry run returns complete content without writes", func(t *testing.T) {
		root := t.TempDir()
		application := newSkillTestApp(t, root, "", root, errorReader{}, false)
		var stdout, stderr bytes.Buffer
		exit := application.Run([]string{"skill", "install", "--scope", "personal", "--agent", "both", "--dry-run"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 {
			t.Fatalf("dry-run = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
		var envelope struct {
			Data skillInstallData `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.Status != "dry_run" || envelope.Data.Content != app.PelletsSkillContent() || len(envelope.Data.Targets) != 2 || envelope.Data.Targets[0].Result != "would_install" {
			t.Fatalf("dry-run data = %#v", envelope.Data)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("dry-run wrote entries %v, %v", entries, err)
		}
	})

	t.Run("JSON conflict identifies all targets", func(t *testing.T) {
		root := t.TempDir()
		for _, relative := range []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"} {
			path := filepath.Join(root, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("different "+relative), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		application := newSkillTestApp(t, root, "", root, errorReader{}, false)
		var stdout, stderr bytes.Buffer
		exit := application.Run([]string{"skill", "install", "--scope", "personal", "--agent", "both", "--yes"}, &stdout, &stderr)
		if exit != 4 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"skill_content_conflict"`) || strings.Count(stderr.String(), `"reason":"content_differs"`) != 2 {
			t.Fatalf("conflict = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
		stdout.Reset()
		stderr.Reset()
		exit = application.Run([]string{"skill", "install", "--scope", "personal", "--agent", "both", "--force", "--yes"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 || strings.Count(stdout.String(), `"result":"replaced"`) != 2 {
			t.Fatalf("forced conflict replacement = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
	})

	t.Run("yes does not suppress replacement confirmation", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "home")
		repository := filepath.Join(temporary, "repository")
		for _, path := range []string{home, repository} {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		target := filepath.Join(repository, ".agents", "skills", "pellets", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := "keep user file\n"
		if err := os.WriteFile(target, []byte(sentinel), 0o644); err != nil {
			t.Fatal(err)
		}
		application := newSkillTestApp(t, home, repository, repository, strings.NewReader("1\n1\nn\n"), true)
		var stdout, stderr bytes.Buffer
		exit := application.Run([]string{"--human", "skill", "install", "--yes"}, &stdout, &stderr)
		if exit != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Replace every differing existing skill file?") || !strings.Contains(stdout.String(), "Installation cancelled") {
			t.Fatalf("replacement cancellation = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != sentinel {
			t.Fatalf("replacement cancellation changed file to %q, %v", got, err)
		}
	})
}

func TestPelletsSkillReferencedCommandAndFlagContract(t *testing.T) {
	t.Parallel()

	commands := []Command{
		AddCommand(app.PelletManager{}), MoveCommand(app.PelletManager{}),
		ListCommand(app.PelletManager{}), StartNextCommand(app.PelletManager{}),
		ReleaseCommand(app.PelletManager{}), CloseCommand(app.PelletManager{}),
		DeferCommand(app.PelletManager{}), ReopenCommand(app.PelletManager{}),
		MemoryCommand(app.MemoryManager{}), ProjectCommand(app.ProjectManager{}),
	}
	application := NewWithCommands("test", commands...)
	parsedExamples := 0
	for _, line := range strings.Split(app.PelletsSkillContent(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pl ") {
			continue
		}
		args, err := splitSkillExample(line)
		if err != nil {
			t.Fatalf("parse example %q: %v", line, err)
		}
		parsed, err := application.parse(args[1:])
		if err != nil {
			t.Errorf("implemented CLI rejected skill example %q: %v", line, err)
			continue
		}
		if parsed.action == actionRun {
			input, err := parsed.command.Parse(parsed.args)
			if err != nil {
				t.Errorf("implemented command parser rejected skill example %q: %v", line, err)
				continue
			}
			if err := validateGlobalOptions(parsed.globals); err != nil {
				t.Errorf("global validation rejected skill example %q: %v", line, err)
			}
			if parsed.command.Validate != nil {
				if err := parsed.command.Validate(parsed.globals, input); err != nil {
					t.Errorf("command validation rejected skill example %q: %v", line, err)
				}
			}
		}
		parsedExamples++
	}
	if parsedExamples < 10 {
		t.Fatalf("parsed only %d command examples; drift coverage is unexpectedly narrow", parsedExamples)
	}

	flagPattern := regexp.MustCompile(`--[a-z][a-z-]*`)
	supportedFlags := map[string]bool{
		"--help": true, "--pretty": true, "--human": true, "--project": true,
		"--external-id": true, "--group": true, "--recover-workspace": true,
		"--yes": true, "--before": true, "--after": true, "--created-by": true,
		"--approved-only": true, "--text": true,
	}
	for _, flag := range flagPattern.FindAllString(app.PelletsSkillContent(), -1) {
		if !supportedFlags[flag] {
			t.Errorf("skill references flag %q without an implemented CLI contract fixture", flag)
		}
	}
	for _, args := range [][]string{
		{"--help"}, {"--pretty", "list"}, {"--human", "list"},
		{"--project", "foo", "project", "show"},
	} {
		if _, err := application.parse(args); err != nil {
			t.Errorf("implemented global parser rejected referenced flag invocation %v: %v", args, err)
		}
	}
}

func newSkillTestApp(t *testing.T, home, repository, working string, stdin io.Reader, terminal bool) *App {
	t.Helper()
	installer := app.SkillInstaller{
		UserHomeDir: func() (string, error) { return home, nil },
		FindGitRoot: func(context.Context, string) (string, error) {
			if repository == "" {
				return "", domain.NewError(domain.NotFound, "git_repository_not_found", "not in Git", nil)
			}
			return repository, nil
		},
	}
	application := NewWithCommands("test", SkillCommand(installer))
	application.workingDirectory = func() (string, error) { return working, nil }
	application.stdin = stdin
	application.isInteractive = func(io.Reader, io.Writer) bool { return terminal }
	return application
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("stdin must not be read")
}

func splitSkillExample(line string) ([]string, error) {
	var arguments []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			arguments = append(arguments, current.String())
			current.Reset()
		}
	}
	for _, character := range line {
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				current.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if unicode.IsSpace(character) {
			flush()
			continue
		}
		current.WriteRune(character)
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quote or escape")
	}
	flush()
	return arguments, nil
}
