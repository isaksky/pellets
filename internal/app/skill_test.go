package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"pellets/internal/domain"
)

func TestPelletsSkillGoldenPortableContract(t *testing.T) {
	t.Parallel()

	content := PelletsSkillContent()
	if !strings.HasPrefix(content, "---\n") || !strings.HasSuffix(content, "\n") {
		t.Fatalf("embedded skill delimiters/newline are invalid: %q", content)
	}
	parts := strings.SplitN(content, "\n---\n", 2)
	if len(parts) != 2 {
		t.Fatal("embedded skill has no closing YAML frontmatter delimiter")
	}
	frontmatter := make(map[string]string)
	for _, line := range strings.Split(strings.TrimPrefix(parts[0], "---\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			t.Fatalf("invalid portable frontmatter line %q", line)
		}
		frontmatter[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(frontmatter) != 2 || frontmatter["name"] != "pellets" {
		t.Fatalf("frontmatter = %#v, want portable name/description only", frontmatter)
	}
	description := frontmatter["description"]
	for _, required := range []string{
		"Use only", "explicitly names", "pl command", "Pellet/Pellets",
		"Do not activate", "task", "issue", "ticket", "queue", "backlog",
		"project", "project-management", "memory", "do not name pl or Pellet/Pellets",
		"Explicit skill invocation",
	} {
		if !strings.Contains(description, required) {
			t.Errorf("description does not contain %q: %q", required, description)
		}
	}

	body := parts[1]
	for _, required := range []string{
		"pl --help", "default compact JSON", "walking upward", "linked worktrees",
		"pl start-next", "pl next", "workspace_already_in_progress",
		"pellet_in_progress_elsewhere", "--recover-workspace", "Keep retries bounded",
		"pl add", "pl move", "pl close", "pl defer", "pl reopen",
		"external-id", "group", "focused", "--created-by agent", "--created-by human",
		"pl memory approve", "Never edit the SQLite database directly", "commit `.pellets`",
		"dependencies", "epics", "agent/PID ownership", "leases", "heartbeats",
		"parallel Markdown task queue",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("skill body does not contain %q", required)
		}
	}
	for _, forbidden := range []string{"Codex should", "Claude should", "MCP", "plugin.json", "AGENTS.md", "CLAUDE.md"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("portable instruction body contains forbidden agent-specific/extra artifact text %q", forbidden)
		}
	}
}

func TestPelletsSkillStaticTriggerFixtures(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("testdata", "skill-triggers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Positive []string `json:"positive"`
		Negative []string `json:"negative"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range fixtures.Positive {
		if !namesPellets(prompt) {
			t.Errorf("positive trigger %q does not explicitly name pl/Pellets", prompt)
		}
	}
	for _, prompt := range fixtures.Negative {
		if namesPellets(prompt) {
			t.Errorf("negative trigger %q unexpectedly names pl/Pellets", prompt)
		}
	}
}

func TestSkillInstallerPlanCoversExactScopeAgentMatrix(t *testing.T) {
	t.Parallel()

	temporary := t.TempDir()
	home := filepath.Join(temporary, "home with spaces 界")
	repository := filepath.Join(temporary, "repository with spaces 界")
	for _, path := range []string{home, repository} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	installer := SkillInstaller{}
	environment := SkillEnvironment{HomeRoot: home, RepositoryRoot: repository}
	tests := []struct {
		scope SkillScope
		agent SkillAgent
		root  string
		rels  []string
	}{
		{SkillScopeRepository, SkillAgentCodex, repository, []string{".agents/skills/pellets/SKILL.md"}},
		{SkillScopeRepository, SkillAgentClaude, repository, []string{".claude/skills/pellets/SKILL.md"}},
		{SkillScopeRepository, SkillAgentBoth, repository, []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"}},
		{SkillScopePersonal, SkillAgentCodex, home, []string{".agents/skills/pellets/SKILL.md"}},
		{SkillScopePersonal, SkillAgentClaude, home, []string{".claude/skills/pellets/SKILL.md"}},
		{SkillScopePersonal, SkillAgentBoth, home, []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"}},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.scope)+"/"+string(test.agent), func(t *testing.T) {
			plan, err := installer.Plan(environment, test.scope, test.agent)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Content != PelletsSkillContent() || len(plan.Targets) != len(test.rels) {
				t.Fatalf("plan = %#v", plan)
			}
			for index, relative := range test.rels {
				want := filepath.Join(test.root, filepath.FromSlash(relative))
				if plan.Targets[index].Path != want || plan.Targets[index].State != "missing" {
					t.Errorf("target %d = %#v, want missing %q", index, plan.Targets[index], want)
				}
			}
			for _, root := range []string{home, repository} {
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("read-only plan created entries under %q: %v", root, entries)
				}
			}
		})
	}
}

func TestSkillInstallerApplyIsIdempotentAndRequiresForceForDifferences(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	installer := SkillInstaller{}
	environment := SkillEnvironment{HomeRoot: root}
	plan, err := installer.Plan(environment, SkillScopePersonal, SkillAgentBoth)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installer.Apply(plan, false)
	if err != nil || installed.Status != "installed" {
		t.Fatalf("first apply = (%#v, %v)", installed, err)
	}
	for _, target := range installed.Targets {
		content, err := os.ReadFile(target.Path)
		if err != nil || string(content) != PelletsSkillContent() {
			t.Fatalf("installed target %q = %q, %v", target.Path, content, err)
		}
	}

	plan, err = installer.Plan(environment, SkillScopePersonal, SkillAgentBoth)
	if err != nil {
		t.Fatal(err)
	}
	idempotent, err := installer.Apply(plan, false)
	if err != nil || idempotent.Status != "idempotent" {
		t.Fatalf("idempotent apply = (%#v, %v)", idempotent, err)
	}

	sentinel := []byte("user-owned differing skill\n")
	if err := os.WriteFile(plan.Targets[0].Path, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(plan.Targets[0].Path, 0o600); err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Stat(plan.Targets[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = installer.Plan(environment, SkillScopePersonal, SkillAgentBoth)
	if err != nil {
		t.Fatal(err)
	}
	_, err = installer.Apply(plan, false)
	if err == nil || domain.PublicError(err).Code != "skill_content_conflict" {
		t.Fatalf("unforced conflict error = %v", err)
	}
	if got, err := os.ReadFile(plan.Targets[0].Path); err != nil || string(got) != string(sentinel) {
		t.Fatalf("conflict changed sentinel to %q, %v", got, err)
	}
	replaced, err := installer.Apply(plan, true)
	if err != nil || replaced.Status != "installed" || replaced.Targets[0].Result != "replaced" || replaced.Targets[1].Result != "idempotent" {
		t.Fatalf("forced replacement = (%#v, %v)", replaced, err)
	}
	if info, err := os.Stat(plan.Targets[0].Path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != replacementInfo.Mode().Perm() {
		t.Fatalf("replacement mode = %v; want preserved platform mode %v", info.Mode(), replacementInfo.Mode())
	}
}

func TestSkillInstallerRealFilesystemFullScopeAgentMatrix(t *testing.T) {
	t.Parallel()

	for _, scope := range []SkillScope{SkillScopeRepository, SkillScopePersonal} {
		for _, agent := range []SkillAgent{SkillAgentCodex, SkillAgentClaude, SkillAgentBoth} {
			scope, agent := scope, agent
			t.Run(string(scope)+"/"+string(agent), func(t *testing.T) {
				t.Parallel()
				temporary := t.TempDir()
				home := filepath.Join(temporary, "home with spaces 界")
				repository := filepath.Join(temporary, "repository with spaces 界")
				for _, path := range []string{home, repository} {
					if err := os.Mkdir(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				root := home
				if scope == SkillScopeRepository {
					root = repository
				}
				unrelated := filepath.Join(root, "unrelated.txt")
				if err := os.WriteFile(unrelated, []byte("preserve me\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				unrelatedInfo, err := os.Stat(unrelated)
				if err != nil {
					t.Fatal(err)
				}
				plan, err := (SkillInstaller{}).Plan(
					SkillEnvironment{HomeRoot: home, RepositoryRoot: repository}, scope, agent,
				)
				if err != nil {
					t.Fatal(err)
				}
				result, err := (SkillInstaller{}).Apply(plan, false)
				if err != nil || result.Status != "installed" {
					t.Fatalf("Apply = (%#v, %v)", result, err)
				}
				wantTargets := 1
				if agent == SkillAgentBoth {
					wantTargets = 2
				}
				if len(result.Targets) != wantTargets {
					t.Fatalf("targets = %#v, want %d", result.Targets, wantTargets)
				}
				for _, target := range result.Targets {
					content, err := os.ReadFile(target.Path)
					if err != nil || string(content) != PelletsSkillContent() {
						t.Errorf("target %q = %q, %v", target.Path, content, err)
					}
				}
				if content, err := os.ReadFile(unrelated); err != nil || string(content) != "preserve me\n" {
					t.Fatalf("unrelated file = %q, %v", content, err)
				}
				if info, err := os.Stat(unrelated); err != nil || info.Mode().Perm() != unrelatedInfo.Mode().Perm() {
					t.Fatalf("unrelated platform mode changed: before %v, after %v, %v", unrelatedInfo, info, err)
				}
				otherRoot := repository
				if scope == SkillScopeRepository {
					otherRoot = home
				}
				entries, err := os.ReadDir(otherRoot)
				if err != nil || len(entries) != 0 {
					t.Fatalf("installation changed unselected root %q: %v, %v", otherRoot, entries, err)
				}
			})
		}
	}
}

func TestSkillInstallerBothRollsBackCreatedAndReplacedTargets(t *testing.T) {
	t.Parallel()

	t.Run("created", func(t *testing.T) {
		root := t.TempDir()
		installer := SkillInstaller{}
		plan, err := installer.Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentBoth)
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		installer.AtomicWrite = func(path string, content []byte, mode fs.FileMode) error {
			if strings.Contains(path, ".claude") && !failed {
				failed = true
				return errors.New("injected second-target failure")
			}
			return atomicWriteSkillFile(path, content, mode)
		}
		_, err = installer.Apply(plan, true)
		if err == nil || domain.PublicError(err).Code != "skill_install_failed" {
			t.Fatalf("apply error = %v", err)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) != 0 {
			t.Fatalf("rollback left entries %v, %v", entries, readErr)
		}
	})

	t.Run("replaced", func(t *testing.T) {
		root := t.TempDir()
		codex := filepath.Join(root, ".agents", "skills", "pellets", "SKILL.md")
		claude := filepath.Join(root, ".claude", "skills", "pellets", "SKILL.md")
		for _, path := range []string{codex, claude} {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		codexOriginal := []byte("codex original\n")
		claudeOriginal := []byte("claude original\n")
		if err := os.WriteFile(codex, codexOriginal, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(claude, claudeOriginal, 0o600); err != nil {
			t.Fatal(err)
		}
		installer := SkillInstaller{}
		plan, err := installer.Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentBoth)
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		installer.AtomicWrite = func(path string, content []byte, mode fs.FileMode) error {
			if path == claude && !failed {
				failed = true
				return errors.New("injected replacement failure")
			}
			return atomicWriteSkillFile(path, content, mode)
		}
		_, err = installer.Apply(plan, true)
		if err == nil {
			t.Fatal("Apply succeeded, want rollback failure result")
		}
		for path, want := range map[string][]byte{codex: codexOriginal, claude: claudeOriginal} {
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != string(want) {
				t.Errorf("restored %q = %q, %v; want %q", path, got, readErr, want)
			}
		}
	})
}

func TestSkillInstallerRejectsSymlinkNonRegularAndProtectedParents(t *testing.T) {
	t.Parallel()

	t.Run("symlink target", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, ".agents", "skills", "pellets", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, target); err != nil {
			if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
		_, err := (SkillInstaller{}).Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentCodex)
		if err == nil || domain.PublicError(err).Code != "skill_target_unsafe" {
			t.Fatalf("symlink error = %v", err)
		}
		if got, err := os.ReadFile(outside); err != nil || string(got) != "outside\n" {
			t.Fatalf("outside target changed to %q, %v", got, err)
		}
	})

	t.Run("symlink parent", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		parent := filepath.Join(root, ".agents")
		if err := os.Symlink(outside, parent); err != nil {
			if runtime.GOOS == "windows" || errors.Is(err, os.ErrPermission) {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
		_, err := (SkillInstaller{}).Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentCodex)
		if err == nil || domain.PublicError(err).Code != "skill_target_unsafe" {
			t.Fatalf("symlink parent error = %v", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("symlink parent redirected writes: %v, %v", entries, err)
		}
	})

	t.Run("non-directory parent", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".agents"), []byte("protected\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := (SkillInstaller{}).Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentCodex)
		if err == nil || domain.PublicError(err).Code != "skill_target_unsafe" {
			t.Fatalf("parent error = %v", err)
		}
	})

	t.Run("directory target", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, ".agents", "skills", "pellets", "SKILL.md")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := (SkillInstaller{}).Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentCodex)
		if err == nil || domain.PublicError(err).Code != "skill_target_unsafe" {
			t.Fatalf("directory target error = %v", err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("read-only parent", func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o555); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(root, 0o755)
			_, err := (SkillInstaller{}).Plan(SkillEnvironment{HomeRoot: root}, SkillScopePersonal, SkillAgentCodex)
			if err == nil || domain.PublicError(err).Code != "skill_target_unsafe" {
				t.Fatalf("permission error = %v", err)
			}
		})
	}
}

func TestSkillInstallerEnvironmentDoesNotDiscoverPelletsDatabase(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	working := filepath.Join(t.TempDir(), "nested", "path")
	if err := os.MkdirAll(working, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCalls := 0
	installer := SkillInstaller{
		UserHomeDir: func() (string, error) { return home, nil },
		FindGitRoot: func(_ context.Context, directory string) (string, error) {
			gitCalls++
			if directory != working {
				t.Fatalf("Git discovery directory = %q, want %q", directory, working)
			}
			return "", domain.NewError(domain.NotFound, "git_repository_not_found", "not in Git", nil)
		},
	}
	environment, err := installer.Environment(context.Background(), working)
	if err != nil || environment.HomeRoot != home || environment.RepositoryRoot != "" || gitCalls != 1 {
		t.Fatalf("Environment = (%#v, %v), Git calls %d", environment, err, gitCalls)
	}
	if _, err := os.Stat(filepath.Join(working, ".pellets")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("environment discovery touched Pellets metadata: %v", err)
	}
}

func namesPellets(prompt string) bool {
	words := strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	})
	for _, word := range words {
		if word == "pl" || word == "pellet" || word == "pellets" {
			return true
		}
	}
	return false
}
