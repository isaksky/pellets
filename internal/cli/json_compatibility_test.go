package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pellets/internal/app"
)

func TestJSONV1ProjectSkillWebAndTypedEmptySuccessGolden(t *testing.T) {
	common := filepath.Join(t.TempDir(), "JSON v1 compatibility with spaces 界")
	repository := filepath.Join(common, "repository ü")
	nested := filepath.Join(repository, "nested", "working")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "init", "--quiet")

	current := common
	application := projectTestApp(&current)
	var success strings.Builder
	appendCompatibilityResult(t, &success, "init-db", 0, runCompatibilitySuccess(t, application, "init-db"), common)
	appendCompatibilityResult(t, &success, "project-list-empty", 0, runCompatibilitySuccess(t, application, "project", "list"), common)

	current = nested
	appendCompatibilityResult(t, &success, "init", 0, runCompatibilitySuccess(t, application, "init", "--code", "compat"), common)
	appendCompatibilityResult(t, &success, "project-list", 0, runCompatibilitySuccess(t, application, "project", "list"), common)
	appendCompatibilityResult(t, &success, "project-show", 0, runCompatibilitySuccess(t, application, "project", "show", "compat"), common)

	home := filepath.Join(common, "personal home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := New("test", SkillCommand(app.SkillInstaller{UserHomeDir: func() (string, error) { return home, nil }}))
	skill.workingDirectory = func() (string, error) { return nested, nil }
	appendCompatibilityResult(
		t,
		&success,
		"skill-install",
		0,
		runCompatibilitySuccess(t, skill, "skill", "install", "--scope", "personal", "--agent", "codex", "--yes"),
		common,
	)

	web := New("test", WebCommand(func(_ context.Context, _ Invocation, _ WebOptions, stdout, _ io.Writer) error {
		_, err := io.WriteString(stdout, "http://127.0.0.1:43123\n")
		return err
	}))
	web.workingDirectory = func() (string, error) { return nested, nil }
	stdout, stderr, exit := runTestApp(web, "web", "--port", "43123", "--no-open")
	if exit != 0 || stderr != "" || stdout != "http://127.0.0.1:43123\n" {
		t.Fatalf("web success = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	fmt.Fprintf(&success, "web exit=0 %s", stdout)
	assertGolden(t, "json-v1-command-success.golden", success.String())

	var empty strings.Builder
	appendCompatibilityResult(t, &empty, "list-empty", 0, runCompatibilitySuccess(t, application, "list"), common)
	appendCompatibilityResult(t, &empty, "search-empty", 0, runCompatibilitySuccess(t, application, "search", "not-present"), common)
	appendCompatibilityResult(t, &empty, "next-empty", 0, runCompatibilitySuccess(t, application, "next"), common)
	appendCompatibilityResult(t, &empty, "start-next-empty", 0, runCompatibilitySuccess(t, application, "start-next"), common)
	appendCompatibilityResult(t, &empty, "memory-list-empty", 0, runCompatibilitySuccess(t, application, "memory", "list"), common)
	appendCompatibilityResult(t, &empty, "memory-search-empty", 0, runCompatibilitySuccess(t, application, "memory", "search", "not-present"), common)
	appendCompatibilityResult(
		t,
		&empty,
		"purge-empty",
		0,
		runCompatibilitySuccess(t, application, "purge", "--project", "compat", "--dry-run"),
		common,
	)
	assertGolden(t, "json-v1-typed-empty.golden", empty.String())
}

func TestJSONV1EveryCommandErrorGoldenAndExitCode(t *testing.T) {
	working := t.TempDir()
	application := New(
		"test",
		InitDBCommand(app.DatabaseInitializer{}),
		InitCommand(app.ProjectManager{}),
		ProjectCommand(app.ProjectManager{}),
		AddCommand(emptyPelletManager()),
		ListCommand(emptyPelletManager()),
		NextCommand(emptyPelletManager()),
		ShowCommand(emptyPelletManager()),
		EditCommand(emptyPelletManager()),
		MoveCommand(emptyPelletManager()),
		StartCommand(emptyPelletManager()),
		StartNextCommand(emptyPelletManager()),
		ReleaseCommand(emptyPelletManager()),
		CloseCommand(emptyPelletManager()),
		ReopenCommand(emptyPelletManager()),
		DeferCommand(emptyPelletManager()),
		SearchCommand(emptyPelletManager()),
		PurgeCommand(emptyPelletManager()),
		MemoryCommand(app.MemoryManager{}),
		SkillCommand(app.SkillInstaller{}),
		WebCommand(func(context.Context, Invocation, WebOptions, io.Writer, io.Writer) error { return nil }),
	)
	workingDirectoryCalls := 0
	application.workingDirectory = func() (string, error) {
		workingDirectoryCalls++
		return working, nil
	}

	tests := []struct {
		label string
		exit  int
		code  string
		args  []string
	}{
		{label: "init-db-error", exit: 2, code: "project_not_allowed", args: []string{"--project", "compat", "init-db"}},
		{label: "init-error", exit: 2, code: "missing_required_flag", args: []string{"init"}},
		{label: "project-list-error", exit: 2, code: "unexpected_argument", args: []string{"project", "list", "extra"}},
		{label: "project-show-error", exit: 2, code: "unexpected_argument", args: []string{"project", "show", "compat", "extra"}},
		{label: "add-error", exit: 2, code: "missing_title", args: []string{"add"}},
		{label: "list-error", exit: 2, code: "invalid_status", args: []string{"list", "--status", "unknown"}},
		{label: "next-error", exit: 2, code: "unexpected_argument", args: []string{"next", "extra"}},
		{label: "show-error", exit: 2, code: "invalid_reference", args: []string{"show", "12"}},
		{label: "edit-error", exit: 2, code: "missing_edit", args: []string{"edit", "compat-1"}},
		{label: "move-error", exit: 2, code: "missing_placement", args: []string{"move", "compat-1"}},
		{label: "start-error", exit: 2, code: "missing_reference", args: []string{"start"}},
		{label: "start-next-error", exit: 2, code: "unexpected_argument", args: []string{"start-next", "extra"}},
		{label: "release-error", exit: 6, code: "confirmation_required", args: []string{"release", "compat-1", "--recover-workspace", "1"}},
		{label: "close-error", exit: 2, code: "recovery_workspace_required", args: []string{"close", "compat-1", "--yes"}},
		{label: "reopen-error", exit: 2, code: "unexpected_argument", args: []string{"reopen", "compat-1", "extra"}},
		{label: "defer-error", exit: 2, code: "invalid_workspace_id", args: []string{"defer", "compat-1", "--recover-workspace", "01", "--yes"}},
		{label: "search-error", exit: 2, code: "missing_query", args: []string{"search"}},
		{label: "purge-error", exit: 2, code: "missing_required_flag", args: []string{"purge", "--dry-run"}},
		{label: "memory-add-error", exit: 2, code: "missing_memory_text", args: []string{"memory", "add"}},
		{label: "memory-list-error", exit: 2, code: "invalid_limit", args: []string{"memory", "list", "--limit", "0"}},
		{label: "memory-show-error", exit: 2, code: "missing_memory_id", args: []string{"memory", "show"}},
		{label: "memory-search-error", exit: 2, code: "missing_query", args: []string{"memory", "search"}},
		{label: "memory-approve-error", exit: 2, code: "invalid_memory_id", args: []string{"memory", "approve", "01"}},
		{label: "memory-remove-error", exit: 6, code: "confirmation_required", args: []string{"memory", "remove", "1"}},
		{label: "skill-install-error", exit: 2, code: "missing_skill_choices", args: []string{"skill", "install"}},
		{label: "web-error", exit: 2, code: "invalid_port", args: []string{"web", "--port", "080"}},
	}

	var golden strings.Builder
	for _, test := range tests {
		stdout, stderr, exit := runTestApp(application, test.args...)
		if exit != test.exit || stdout != "" || !strings.Contains(stderr, `"code":"`+test.code+`"`) {
			t.Fatalf("%s = exit %d stdout %q stderr %q, want exit %d code %s", test.label, exit, stdout, stderr, test.exit, test.code)
		}
		appendCompatibilityResult(t, &golden, test.label, exit, stderr, "")
	}
	if workingDirectoryCalls != 1 {
		t.Fatalf("invalid compatibility commands crossed discovery %d times, want only skill install's documented environment lookup", workingDirectoryCalls)
	}
	assertGolden(t, "json-v1-command-errors.golden", golden.String())
}

func TestJSONV1GoldenManifestCoversEveryPublicResultName(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".golden") {
			continue
		}
		content, err := os.ReadFile(filepath.Join("testdata", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		fixtures.Write(content)
	}
	for _, command := range []string{
		"init-db", "init", "project list", "project show", "add", "list", "next", "show", "edit", "move",
		"start", "start-next", "release", "close", "reopen", "defer", "search", "purge", "memory add",
		"memory list", "memory show", "memory search", "memory approve", "memory remove", "skill install",
	} {
		if !strings.Contains(fixtures.String(), `"command":"`+command+`"`) {
			t.Errorf("JSON v1 golden fixtures do not cover successful command %q", command)
		}
	}
	for _, rawContract := range []string{
		"web exit=0 http://127.0.0.1:43123\n",
		"Pellets is a local task queue for coding agents.",
		"pl test (JSON schema 1)\n",
	} {
		if !strings.Contains(fixtures.String(), rawContract) {
			t.Errorf("golden fixtures do not cover raw success contract %q", rawContract)
		}
	}
	for _, label := range []string{"project-list-empty", "list-empty", "search-empty", "next-empty", "start-next-empty", "memory-list-empty", "memory-search-empty", "purge-empty"} {
		if !strings.Contains(fixtures.String(), label+" exit=0 ") {
			t.Errorf("JSON v1 golden fixtures do not cover typed empty result %q", label)
		}
	}
	for _, label := range []string{
		"init-db", "init", "project-list", "project-show", "add", "list", "next", "show", "edit", "move",
		"start", "start-next", "release", "close", "reopen", "defer", "search", "purge", "memory-add",
		"memory-list", "memory-show", "memory-search", "memory-approve", "memory-remove", "skill-install", "web",
	} {
		if !strings.Contains(fixtures.String(), label+"-error exit=") {
			t.Errorf("JSON v1 golden fixtures do not cover error/exit contract %q", label)
		}
	}
}

func runCompatibilitySuccess(t *testing.T, application *App, args ...string) string {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, args...)
	if exit != 0 || stderr != "" || stdout == "" {
		t.Fatalf("pl %s = exit %d stdout %q stderr %q", strings.Join(args, " "), exit, stdout, stderr)
	}
	return stdout
}

func appendCompatibilityResult(t *testing.T, output *strings.Builder, label string, exit int, raw, root string) {
	t.Helper()
	if strings.Count(raw, "\n") != 1 || !strings.HasSuffix(raw, "\n") {
		t.Fatalf("%s output is not one compact JSON line: %q", label, raw)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode %s JSON %q: %v", label, raw, err)
	}
	normalizePelletTimestamps(value)
	normalizeCompatibilityPaths(value, filepath.Clean(root))
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(output, "%s exit=%d %s", label, exit, encoded.String())
}

func normalizeCompatibilityPaths(value any, root string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok && root != "" {
				cleaned := filepath.Clean(text)
				if cleaned == root || strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
					relative, err := filepath.Rel(root, cleaned)
					if err == nil {
						if relative == "." {
							typed[key] = "<root>"
						} else {
							typed[key] = filepath.ToSlash(filepath.Join("<root>", relative))
						}
					}
				}
			}
			normalizeCompatibilityPaths(child, root)
		}
	case []any:
		for _, child := range typed {
			normalizeCompatibilityPaths(child, root)
		}
	}
}
