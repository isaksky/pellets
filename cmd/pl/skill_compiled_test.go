package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompiledSkillInstallerWithoutPelletsDatabase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("compiled skill tests require native Git: %v", err)
	}
	executable := buildFoundationExecutable(t)

	t.Run("personal installation outside Git", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "personal home with spaces 界")
		working := filepath.Join(temporary, "outside Git")
		for _, path := range []string{home, working} {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		result := runCompiledSkill(t, executable, working, home,
			"skill", "install", "--scope", "personal", "--agent", "codex", "--yes",
		)
		if result.exit != 0 || result.stderr != "" {
			t.Fatalf("personal install = %#v", result)
		}
		var envelope struct {
			SchemaVersion int    `json:"schema_version"`
			Command       string `json:"command"`
			Data          struct {
				Status  string `json:"status"`
				Scope   string `json:"scope"`
				Agent   string `json:"agent"`
				Targets []struct {
					Path   string `json:"path"`
					Result string `json:"result"`
				} `json:"targets"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		wantPath := filepath.Join(home, ".agents", "skills", "pellets", "SKILL.md")
		if envelope.SchemaVersion != 1 || envelope.Command != "skill install" || envelope.Data.Status != "installed" || envelope.Data.Scope != "personal" || envelope.Data.Agent != "codex" || len(envelope.Data.Targets) != 1 || envelope.Data.Targets[0].Path != wantPath || envelope.Data.Targets[0].Result != "installed" {
			t.Fatalf("personal envelope = %#v", envelope)
		}
		assertCompiledPortableSkill(t, wantPath)
		if entries, err := os.ReadDir(working); err != nil || len(entries) != 0 {
			t.Fatalf("personal install changed working directory: %v, %v", entries, err)
		}
	})

	t.Run("repository both from nested directory", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "home")
		repository := filepath.Join(temporary, "repository with spaces 界")
		nested := filepath.Join(repository, "nested", "current")
		if err := os.Mkdir(home, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		runDiscoveryGitForCompiledSkill(t, repository, "init", "--quiet")
		indexBefore := compiledSkillGitOutput(t, repository, "ls-files", "--stage", "-z")
		canonicalRepository, err := filepath.EvalSymlinks(repository)
		if err != nil {
			t.Fatal(err)
		}

		result := runCompiledSkill(t, executable, nested, home,
			"skill", "install", "--scope", "repo", "--agent", "both", "--yes",
		)
		repositoryJSON, _ := json.Marshal(canonicalRepository)
		if result.exit != 0 || result.stderr != "" || !strings.Contains(result.stdout, `"repository_root":`+string(repositoryJSON)) {
			t.Fatalf("repository install = %#v", result)
		}
		for _, relative := range []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"} {
			assertCompiledPortableSkill(t, filepath.Join(canonicalRepository, filepath.FromSlash(relative)))
		}
		if indexAfter := compiledSkillGitOutput(t, repository, "ls-files", "--stage", "-z"); !bytes.Equal(indexAfter, indexBefore) {
			t.Fatalf("repository installer changed index from %q to %q", indexBefore, indexAfter)
		}
		for _, path := range []string{".gitignore", "AGENTS.md", "CLAUDE.md", ".pellets"} {
			if _, err := os.Stat(filepath.Join(repository, path)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("repository installer created protected path %q: %v", path, err)
			}
		}
		status := string(compiledSkillGitOutput(t, repository, "status", "--porcelain=v1", "--untracked-files=all"))
		for _, want := range []string{"?? .agents/skills/pellets/SKILL.md", "?? .claude/skills/pellets/SKILL.md"} {
			if !strings.Contains(status, want) {
				t.Errorf("Git status %q does not contain %q", status, want)
			}
		}
	})

	t.Run("full scope and agent matrix", func(t *testing.T) {
		for _, test := range []struct {
			scope string
			agent string
			rels  []string
		}{
			{"repo", "codex", []string{".agents/skills/pellets/SKILL.md"}},
			{"repo", "claude", []string{".claude/skills/pellets/SKILL.md"}},
			{"repo", "both", []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"}},
			{"personal", "codex", []string{".agents/skills/pellets/SKILL.md"}},
			{"personal", "claude", []string{".claude/skills/pellets/SKILL.md"}},
			{"personal", "both", []string{".agents/skills/pellets/SKILL.md", ".claude/skills/pellets/SKILL.md"}},
		} {
			test := test
			t.Run(test.scope+"/"+test.agent, func(t *testing.T) {
				temporary := t.TempDir()
				home := filepath.Join(temporary, "home 界")
				repository := filepath.Join(temporary, "repository 界")
				working := filepath.Join(repository, "nested")
				for _, path := range []string{home, working} {
					if err := os.MkdirAll(path, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				runDiscoveryGitForCompiledSkill(t, repository, "init", "--quiet")
				result := runCompiledSkill(t, executable, working, home,
					"skill", "install", "--scope", test.scope, "--agent", test.agent, "--yes",
				)
				if result.exit != 0 || result.stderr != "" {
					t.Fatalf("matrix result = %#v", result)
				}
				root := home
				if test.scope == "repo" {
					var err error
					root, err = filepath.EvalSymlinks(repository)
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, relative := range test.rels {
					assertCompiledPortableSkill(t, filepath.Join(root, filepath.FromSlash(relative)))
				}
				if strings.Count(result.stdout, `"result":"installed"`) != len(test.rels) {
					t.Fatalf("matrix target results = %q, want %d installed", result.stdout, len(test.rels))
				}
			})
		}
	})

	t.Run("linked worktree uses its own repository scope", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "home")
		mainRoot := filepath.Join(temporary, "main worktree")
		linkedRoot := filepath.Join(temporary, "linked worktree 界")
		if err := os.Mkdir(home, 0o755); err != nil {
			t.Fatal(err)
		}
		createFoundationRepository(t, mainRoot)
		if output, err := foundationGitCommand(mainRoot, "worktree", "add", "--quiet", "-b", "skill-linked", linkedRoot); err != nil {
			t.Fatalf("add linked worktree: %v\n%s", err, output)
		}
		nested := filepath.Join(linkedRoot, "nested", "current")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		result := runCompiledSkill(t, executable, nested, home,
			"skill", "install", "--scope", "repo", "--agent", "claude", "--yes",
		)
		if result.exit != 0 || result.stderr != "" {
			t.Fatalf("linked-worktree install = %#v", result)
		}
		canonicalLinked, err := filepath.EvalSymlinks(linkedRoot)
		if err != nil {
			t.Fatal(err)
		}
		assertCompiledPortableSkill(t, filepath.Join(canonicalLinked, ".claude", "skills", "pellets", "SKILL.md"))
		if _, err := os.Stat(filepath.Join(mainRoot, ".claude")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("linked-worktree install changed main worktree: %v", err)
		}
	})

	t.Run("dry run and validation are write free", func(t *testing.T) {
		temporary := t.TempDir()
		home := filepath.Join(temporary, "home")
		working := filepath.Join(temporary, "working")
		for _, path := range []string{home, working} {
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		dryRun := runCompiledSkill(t, executable, working, home,
			"skill", "install", "--scope", "personal", "--agent", "both", "--dry-run",
		)
		if dryRun.exit != 0 || dryRun.stderr != "" || !strings.Contains(dryRun.stdout, `"status":"dry_run"`) || !strings.Contains(dryRun.stdout, `"content":"---\nname: pellets\n`) {
			t.Fatalf("compiled dry run = %#v", dryRun)
		}
		missing := runCompiledSkill(t, executable, working, home, "skill", "install")
		if missing.exit != 2 || missing.stdout != "" || !strings.Contains(missing.stderr, `"code":"missing_skill_choices"`) {
			t.Fatalf("compiled missing choices = %#v", missing)
		}
		for _, root := range []string{home, working} {
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("write-free invocation changed %q: %v, %v", root, entries, err)
			}
		}
	})
}

func runCompiledSkill(t *testing.T, executable, directory, home string, args ...string) foundationResult {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Dir = directory
	command.Env = compiledSkillEnvironment(home)
	command.Stdin = strings.NewReader("input must not be read in JSON mode")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return foundationProcessResult(t, &stdout, &stderr, err)
}

func compiledSkillEnvironment(home string) []string {
	environment := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "HOME") || strings.EqualFold(key, "USERPROFILE") || strings.EqualFold(key, "HOMEDRIVE") || strings.EqualFold(key, "HOMEPATH") {
			continue
		}
		environment = append(environment, entry)
	}
	environment = append(environment, "HOME="+home, "USERPROFILE="+home, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if volume := filepath.VolumeName(home); volume != "" {
		environment = append(environment, "HOMEDRIVE="+volume, "HOMEPATH="+strings.TrimPrefix(home, volume))
	}
	return environment
}

func assertCompiledPortableSkill(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.HasPrefix(text, "---\nname: pellets\ndescription: ") || !strings.Contains(text, "\n---\n\n# Pellets\n") {
		t.Fatalf("compiled installed skill %q is not portable: %q", path, text)
	}
}

func runDiscoveryGitForCompiledSkill(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func compiledSkillGitOutput(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}
