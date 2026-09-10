package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func stringPointer(value string) *string { return &value }
func intPointer(value int) *int          { return &value }

func installFakePelletsTool(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	name := "pl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(directory, name)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(executable, path); err != nil {
		source, openErr := os.Open(executable)
		if openErr != nil {
			t.Fatal(openErr)
		}
		destination, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if createErr != nil {
			source.Close()
			t.Fatal(createErr)
		}
		_, copyErr := io.Copy(destination, source)
		closeErr := errors.Join(source.Close(), destination.Close())
		if err := errors.Join(copyErr, closeErr); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PELLETS_CODEX_TEST_TOOL_MODE", "ready")
	t.Setenv("PELLETS_CODEX_TEST_ORIGINAL_EXE", executable)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	t.Setenv("PATH", directory)
	return path
}

func pelletsToolPeer(mode string) {
	if !reflect.DeepEqual(os.Args[1:], []string{"--version"}) {
		panic("wrong Pellets tool probe arguments")
	}
	workspace, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(workspace); evalErr == nil {
		workspace = evaluated
	}
	if expected := os.Getenv("PELLETS_CODEX_TEST_TOOL_CWD"); expected != "" && workspace != expected {
		panic("wrong Pellets tool probe cwd")
	}
	if marker := os.Getenv("PELLETS_CODEX_TEST_TOOL_MARKER"); marker != "" {
		if os.Getenv("PELLETS_CODEX_TEST_TOOL_ENV") != "inherited" {
			panic("Pellets tool probe did not inherit its environment")
		}
		if err := os.WriteFile(marker, []byte("invoked"), 0600); err != nil {
			panic(err)
		}
	}
	if mode == "descendant-hang" {
		spawnPelletsToolDescendant()
		time.Sleep(30 * time.Second)
	}
}

func spawnPelletsToolDescendant() {
	executable := os.Getenv("PELLETS_CODEX_TEST_ORIGINAL_EXE")
	command := exec.Command(executable)
	command.Env = replaceTestEnvironment(map[string]string{
		"PELLETS_CODEX_TEST_TOOL_MODE": "",
		"PELLETS_CODEX_TEST_PEER":      "descendant-grandchild",
		"GORACE":                       "atexit_sleep_ms=0",
	})
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	detachTestDescendant(command)
	if err := command.Start(); err != nil {
		panic(err)
	}
}

func replaceTestEnvironment(replacements map[string]string) []string {
	environment := make([]string, 0, len(os.Environ())+len(replacements))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := replacements[key]; !replaced {
			environment = append(environment, entry)
		}
	}
	for key, value := range replacements {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func TestResolveRunSettingsPrecedenceDefaultsAndRecommendation(t *testing.T) {
	saved := WorkspaceRunSettings{
		Executable: "saved-codex", Model: "saved-model", ReasoningEffort: "medium",
		Limits: RunLimits{MaxMessageBytes: 4096, EventBuffer: 8, MaxPending: 7, StderrBytes: 1024},
	}
	settings, err := ResolveRunSettings(saved, RunOverrides{
		Executable: stringPointer("override-codex"), Model: stringPointer(""), ReasoningEffort: stringPointer("high"),
		Limits: &RunLimitOverrides{EventBuffer: intPointer(0), MaxPending: intPointer(9)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := RunSettings{
		Executable: "override-codex", ReasoningEffort: "high",
		Limits: RunLimits{MaxMessageBytes: 4096, EventBuffer: defaultEventBuffer, MaxPending: 9, StderrBytes: 1024},
	}
	if !reflect.DeepEqual(settings, want) {
		t.Fatalf("settings = %#v, want %#v", settings, want)
	}
	defaults, err := ResolveRunSettings(WorkspaceRunSettings{}, RunOverrides{})
	if err != nil || defaults.Executable != "codex" || defaults.Model != "" || defaults.ReasoningEffort != "" {
		t.Fatalf("defaults = %#v, %v", defaults, err)
	}
	if recommended := RecommendedSettings(); recommended.Model != RecommendedModel || recommended.ReasoningEffort != RecommendedReasoningEffort {
		t.Fatal(recommended)
	}
}

func TestResolveRunSettingsRejectsMalformedValuesAndLimits(t *testing.T) {
	for _, test := range []WorkspaceRunSettings{
		{Executable: " codex"}, {Model: "model\n"}, {ReasoningEffort: " high"},
		{Limits: RunLimits{MaxMessageBytes: 255}}, {Limits: RunLimits{EventBuffer: -1}},
		{Limits: RunLimits{EventBuffer: maximumEventBuffer + 1}},
	} {
		if _, err := ResolveRunSettings(test, RunOverrides{}); !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("accepted %#v: %v", test, err)
		}
	}
}

func TestPelletsToolPrerequisiteUsesInheritedPath(t *testing.T) {
	t.Run("available", func(t *testing.T) {
		workspace := t.TempDir()
		workspace, _ = filepath.EvalSymlinks(workspace)
		marker := filepath.Join(t.TempDir(), "invoked")
		t.Setenv("PELLETS_CODEX_TEST_TOOL_CWD", workspace)
		t.Setenv("PELLETS_CODEX_TEST_TOOL_ENV", "inherited")
		t.Setenv("PELLETS_CODEX_TEST_TOOL_MARKER", marker)
		installFakePelletsTool(t)
		if err := requirePelletsTool(context.Background(), workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("pl --version was not invoked in the effective child environment: %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if err := requirePelletsTool(context.Background(), t.TempDir()); !errors.Is(err, ErrToolUnavailable) || !strings.Contains(err.Error(), "inherited PATH") {
			t.Fatalf("requirePelletsTool = %v", err)
		}
	})
}

func TestPelletsToolPrerequisiteBoundsDescendantCleanup(t *testing.T) {
	for _, action := range []string{"timeout", "cancel"} {
		t.Run(action, func(t *testing.T) {
			workspace := t.TempDir()
			workspace, _ = filepath.EvalSymlinks(workspace)
			readyPath := filepath.Join(t.TempDir(), "descendant.json")
			installFakePelletsTool(t)
			t.Setenv("PELLETS_CODEX_TEST_TOOL_MODE", "descendant-hang")
			t.Setenv("PELLETS_TEST_DESCENDANT_FILE", readyPath)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 10 * time.Second
			if action == "timeout" {
				timeout = 2 * time.Second
			}
			done := make(chan error, 1)
			go func() { done <- requirePelletsToolWithin(ctx, workspace, timeout) }()

			deadline := time.Now().Add(3 * time.Second)
			var data []byte
			for {
				data, _ = os.ReadFile(readyPath)
				if json.Valid(data) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Pellets tool descendant readiness timed out")
				}
				time.Sleep(time.Millisecond)
			}
			address := trackDescendant(t, data)
			if action == "cancel" {
				cancel()
			}
			select {
			case err := <-done:
				if action == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error = %v", err)
				}
				if action == "timeout" && (!errors.Is(err, ErrToolUnavailable) || !strings.Contains(err.Error(), "timed out")) {
					t.Fatalf("timeout error = %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("Pellets tool process-tree cleanup hung")
			}
			assertDescendantStopped(t, address)
		})
	}
}

func TestPrepareRunUsesLocalAccountCatalogAndNarrowDatabaseAccess(t *testing.T) {
	installFakePelletsTool(t)
	t.Setenv("PELLETS_CODEX_TEST_PEER", "preflight-ready")
	temporary := t.TempDir()
	workspace := filepath.Join(temporary, "worktree")
	databaseDir := filepath.Join(temporary, "shared", ".pellets")
	existingRoot := filepath.Join(temporary, "existing-tool-root")
	for _, path := range []string{workspace, databaseDir, existingRoot} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	database := filepath.Join(databaseDir, "pellets.db")
	if err := os.WriteFile(database, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PELLETS_CODEX_TEST_WRITABLE_ROOT", existingRoot)
	workspace, _ = filepath.EvalSymlinks(workspace)
	databaseDir, _ = filepath.EvalSymlinks(databaseDir)
	existingRoot, _ = filepath.EvalSymlinks(existingRoot)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prepared, err := PrepareRun(ctx, PrepareOptions{
		WorkspaceDir: workspace, DatabasePath: database,
		Saved: WorkspaceRunSettings{Executable: executable}, Overrides: RunOverrides{
			Model: stringPointer(RecommendedModel), ReasoningEffort: stringPointer(RecommendedReasoningEffort),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Client.Close()
	if !prepared.Account.Ready || prepared.Account.Type != "chatgpt" || len(prepared.Models) != 2 {
		t.Fatalf("preflight = account %#v, models %#v", prepared.Account, prepared.Models)
	}
	if strings.Contains(strings.TrimSpace(string(mustJSON(t, prepared))), "must-not-escape") {
		t.Fatal("account email escaped into prepared run state")
	}

	thread := prepared.ThreadStartParams()
	if thread["approvalPolicy"] != "on-request" || thread["approvalsReviewer"] != "auto_review" || thread["sandbox"] != "workspace-write" || thread["cwd"] != workspace || thread["model"] != RecommendedModel {
		t.Fatalf("thread params = %#v", thread)
	}
	config := thread["config"].(map[string]any)
	sandboxConfig := config["sandbox_workspace_write"].(map[string]any)
	if roots := stringSlice(sandboxConfig["writable_roots"]); !reflect.DeepEqual(roots, []string{existingRoot, databaseDir}) {
		t.Fatalf("thread writable roots = %#v", roots)
	}
	turn, err := prepared.TurnStartParams("thread-1", []any{map[string]any{"type": "text", "text": "work"}})
	if err != nil {
		t.Fatal(err)
	}
	policy := turn["sandboxPolicy"].(map[string]any)
	if policy["type"] != "workspaceWrite" || policy["networkAccess"] != false || policy["excludeSlashTmp"] != true || policy["excludeTmpdirEnvVar"] != true {
		t.Fatalf("turn sandbox policy = %#v", policy)
	}
	if roots := stringSlice(policy["writableRoots"]); !reflect.DeepEqual(roots, []string{existingRoot, databaseDir}) {
		t.Fatalf("turn writable roots = %#v", roots)
	}
	if turn["effort"] != RecommendedReasoningEffort || turn["model"] != RecommendedModel {
		t.Fatalf("turn settings = %#v", turn)
	}
	thread["cwd"] = "mutated"
	if prepared.ThreadStartParams()["cwd"] != workspace {
		t.Fatal("thread parameter copy aliases prepared state")
	}
}

func TestPrepareRunPreservesNormalModelDefaults(t *testing.T) {
	installFakePelletsTool(t)
	t.Setenv("PELLETS_CODEX_TEST_PEER", "preflight-ready")
	workspace := t.TempDir()
	databaseDir := filepath.Join(workspace, ".pellets")
	if err := os.Mkdir(databaseDir, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(databaseDir, "pellets.db")
	if err := os.WriteFile(database, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	prepared, err := PrepareRun(context.Background(), PrepareOptions{
		WorkspaceDir: workspace, DatabasePath: database, Saved: WorkspaceRunSettings{Executable: executable},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Client.Close()
	thread := prepared.ThreadStartParams()
	if _, exists := thread["model"]; exists {
		t.Fatal("normal model default was replaced")
	}
	if _, exists := thread["config"]; exists {
		t.Fatal("in-workspace database added a writable root override")
	}
	turn, _ := prepared.TurnStartParams("thread", nil)
	if _, exists := turn["model"]; exists {
		t.Fatal("normal turn model default was replaced")
	}
	if _, exists := turn["effort"]; exists {
		t.Fatal("normal reasoning default was replaced")
	}
}

func TestPrepareRunStopsOnLoginPolicyAndEffortFailures(t *testing.T) {
	installFakePelletsTool(t)
	workspace := t.TempDir()
	database := filepath.Join(workspace, "pellets.db")
	if err := os.WriteFile(database, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	base := PrepareOptions{WorkspaceDir: workspace, DatabasePath: database, Saved: WorkspaceRunSettings{Executable: executable}}
	for _, test := range []struct {
		mode    string
		options PrepareOptions
		want    error
	}{
		{"preflight-signed-out", base, ErrUnauthenticated},
		{"preflight-policy-blocked", base, ErrPolicyUnavailable},
		{"preflight-reviewer-blocked", base, ErrPolicyUnavailable},
		{"preflight-ready", PrepareOptions{WorkspaceDir: workspace, DatabasePath: database, Saved: WorkspaceRunSettings{Executable: executable, Model: RecommendedModel, ReasoningEffort: "ultra"}}, ErrInvalidSettings},
		{"preflight-ready", PrepareOptions{WorkspaceDir: workspace, DatabasePath: database, Saved: WorkspaceRunSettings{Executable: executable, Model: "future-model", ReasoningEffort: "high"}}, ErrInvalidSettings},
	} {
		t.Run(test.mode+test.options.Saved.Model+test.options.Saved.ReasoningEffort, func(t *testing.T) {
			t.Setenv("PELLETS_CODEX_TEST_PEER", test.mode)
			prepared, err := PrepareRun(context.Background(), test.options)
			if prepared != nil || !errors.Is(err, test.want) {
				t.Fatalf("PrepareRun = (%#v, %v), want %v", prepared, err, test.want)
			}
		})
	}

	// Model identifiers remain open-ended when there is no explicit effort to validate.
	t.Setenv("PELLETS_CODEX_TEST_PEER", "preflight-ready")
	base.Saved.Model = "future-model"
	prepared, err := PrepareRun(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Client.Close()
}

func TestPrepareRunStopsWhenPelletsIsMissingFromChildPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("PELLETS_CODEX_TEST_PEER", "preflight-ready")
	workspace := t.TempDir()
	database := filepath.Join(workspace, "pellets.db")
	if err := os.WriteFile(database, []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareRun(context.Background(), PrepareOptions{
		WorkspaceDir: workspace, DatabasePath: database,
		Saved: WorkspaceRunSettings{Executable: executable},
	})
	if prepared != nil || !errors.Is(err, ErrToolUnavailable) || !strings.Contains(err.Error(), "inherited PATH") {
		t.Fatalf("PrepareRun = (%#v, %v), want actionable missing-pl error", prepared, err)
	}
}

func stringSlice(value any) []string {
	values := value.([]any)
	result := make([]string, len(values))
	for index := range values {
		result[index] = values[index].(string)
	}
	return result
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
