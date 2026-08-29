package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/app"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage/sqlite"
)

func TestPelletCommandsAcrossMainAndLinkedWorktreesJSONGolden(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "pellet worktree database 界")
	mainWorkTree := filepath.Join(common, "main")
	linkedWorkTree := filepath.Join(common, "linked")
	unregisteredWorkTree := filepath.Join(common, "unregistered")
	if err := os.MkdirAll(mainWorkTree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(mainWorkTree, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "add", "README")
	runGitTest(
		t, mainWorkTree,
		"-c", "user.name=Pellets Test", "-c", "user.email=pellets@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture",
	)
	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "pellet-command-linked", linkedWorkTree)
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := mainWorkTree
	application := projectTestApp(&current)
	mainProject := runProjectInit(t, application, "shared")
	current = linkedWorkTree
	linkedProject := runProjectInit(t, application, "shared")
	if len(linkedProject.Workspaces) != 2 || linkedProject.Workspaces[0].ID != mainProject.Workspaces[0].ID {
		t.Fatalf("linked registration = %#v", linkedProject)
	}

	var outputs strings.Builder
	application.stdin = strings.NewReader("stdin description\n")
	appendPelletGolden(t, &outputs, "add-open", runPelletCommand(t, application,
		"add", "First from main", "--description-file", "-", "--external-id", "Case:Exact", "--group", "Rollout/A"))

	current = linkedWorkTree
	appendPelletGolden(t, &outputs, "add-placed", runPelletCommand(t, application,
		"--project", "shared", "add", "Second from linked", "--after", "shared-1", "--external-id", "Case:Exact", "--group", "Rollout/A"))
	appendPelletGolden(t, &outputs, "add-deferred", runPelletCommand(t, application,
		"add", "Maybe later", "--maybe-later", "--external-id", "Case:Exact", "--group", "Rollout/A"))
	appendPelletGolden(t, &outputs, "move", runPelletCommand(t, application,
		"move", "shared-2", "--after", "shared-1"))

	databasePath := discovery.DatabasePath(common)
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		UPDATE pellets
		SET status = 'in_progress', workspace_id = ?
		WHERE project_id = 1 AND number = 1`, mainProject.Workspaces[0].ID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	appendPelletGolden(t, &outputs, "list-default", runPelletCommand(t, application, "list"))
	current = mainWorkTree
	beforeNext := capturePelletLogicalState(t, databasePath)
	appendPelletGolden(t, &outputs, "next-resume", runPelletCommand(t, application,
		"next", "--external-id", "does-not-match", "--group", "does-not-match"))
	afterNext := capturePelletLogicalState(t, databasePath)
	if !reflect.DeepEqual(afterNext, beforeNext) {
		t.Fatalf("read-only next changed persistent state:\nbefore=%q\nafter=%q", beforeNext, afterNext)
	}

	current = linkedWorkTree
	appendPelletGolden(t, &outputs, "next-open", runPelletCommand(t, application,
		"next", "--external-id", "Case:Exact", "--group", "Rollout/A"))
	appendPelletGolden(t, &outputs, "show", runPelletCommand(t, application, "show", "shared-1"))

	descriptionPath := filepath.Join(common, "edited description 世界.txt")
	if err := os.WriteFile(descriptionPath, []byte("edited from file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendPelletGolden(t, &outputs, "edit", runPelletCommand(t, application,
		"edit", "shared-2", "--title", "Second edited", "--description-file", descriptionPath,
		"--clear-external-id", "--clear-group"))
	appendPelletGolden(t, &outputs, "list-status", runPelletCommand(t, application,
		"list", "--status", "maybe_later"))
	appendPelletGolden(t, &outputs, "list-all", runPelletCommand(t, application, "list", "--all"))
	appendPelletGolden(t, &outputs, "list-exact-limit", runPelletCommand(t, application,
		"list", "--external-id", "Case:Exact", "--group", "Rollout/A", "--limit", "1"))
	appendPelletGolden(t, &outputs, "list-empty", runPelletCommand(t, application,
		"list", "--external-id", "case:exact"))
	appendPelletGolden(t, &outputs, "next-none", runPelletCommand(t, application,
		"next", "--external-id", "Case:Exact", "--group", "Rollout/A"))
	assertGolden(t, "pellet-worktrees.golden", outputs.String())

	stdout, stderr, exit := runTestApp(application, "--human", "list")
	if exit != 0 || stderr != "" {
		t.Fatalf("human list = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	assertGolden(t, "pellet-human-list.golden", stdout)
	stdout, stderr, exit = runTestApp(application, "--human", "next", "--external-id", "missing")
	if exit != 0 || stderr != "" {
		t.Fatalf("human empty next = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	assertGolden(t, "pellet-human-next-empty.golden", stdout)

	startedLinked := runPelletCommand(t, application, "start", "shared-2")
	if repeated := runPelletCommand(t, application, "start", "shared-2"); repeated != startedLinked {
		t.Fatalf("idempotent CLI start changed output:\nfirst=%s\nrepeat=%s", startedLinked, repeated)
	}
	stdout, stderr, exit = runTestApp(application, "start", "shared-1")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"pellet_in_progress_elsewhere"`) {
		t.Fatalf("cross-workspace start = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	stdout, stderr, exit = runTestApp(application, "close", "shared-1")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"pellet_in_progress_elsewhere"`) {
		t.Fatalf("cross-workspace close = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	mainWorkspaceID := mainProject.Workspaces[0].ID
	linkedWorkspaceID := linkedProject.Workspaces[1].ID
	stateBeforeUnconfirmed := capturePelletLogicalState(t, databasePath)
	stdout, stderr, exit = runTestApp(application, "close", "shared-1", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10))
	if exit != 6 || stdout != "" || !strings.Contains(stderr, `"code":"confirmation_required"`) {
		t.Fatalf("unconfirmed recovery close = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if stateAfter := capturePelletLogicalState(t, databasePath); !reflect.DeepEqual(stateAfter, stateBeforeUnconfirmed) {
		t.Fatalf("unconfirmed recovery changed state:\nbefore=%q\nafter=%q", stateBeforeUnconfirmed, stateAfter)
	}
	stdout, stderr, exit = runTestApp(application, "close", "shared-1", "--recover-workspace", strconv.FormatInt(linkedWorkspaceID, 10), "--yes")
	if exit != 4 || stdout != "" || !strings.Contains(stderr, `"code":"recovery_workspace_mismatch"`) {
		t.Fatalf("mismatched recovery close = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	recoveredClose := runPelletCommand(t, application, "close", "shared-1", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10), "--yes")
	var recoveredEnvelope struct {
		Data struct {
			ID                 string               `json:"id"`
			Status             domain.PelletStatus  `json:"status"`
			Priority           *int64               `json:"priority"`
			Workspace          *pelletWorkspaceData `json:"workspace"`
			CompletedAt        *string              `json:"completed_at"`
			RecoveredWorkspace *pelletWorkspaceData `json:"recovered_workspace"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(recoveredClose), &recoveredEnvelope); err != nil {
		t.Fatal(err)
	}
	if recoveredEnvelope.Data.ID != "shared-1" || recoveredEnvelope.Data.Status != domain.PelletClosed || recoveredEnvelope.Data.Priority != nil || recoveredEnvelope.Data.Workspace != nil || recoveredEnvelope.Data.CompletedAt == nil || recoveredEnvelope.Data.RecoveredWorkspace == nil || recoveredEnvelope.Data.RecoveredWorkspace.ID != mainWorkspaceID {
		t.Fatalf("recovered close JSON = %#v; raw=%s", recoveredEnvelope, recoveredClose)
	}
	repeatedClose := runPelletCommand(t, application, "close", "shared-1", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10), "--yes")
	if !strings.Contains(repeatedClose, `"completed_at":"`+*recoveredEnvelope.Data.CompletedAt+`"`) || strings.Contains(repeatedClose, `"recovered_workspace"`) {
		t.Fatalf("idempotent close output = %s, first = %s", repeatedClose, recoveredClose)
	}

	runPelletCommand(t, application, "reopen", "shared-1")
	deferred := runPelletCommand(t, application, "defer", "shared-1")
	if repeated := runPelletCommand(t, application, "defer", "shared-1"); repeated != deferred {
		t.Fatalf("idempotent CLI defer changed output:\nfirst=%s\nrepeat=%s", deferred, repeated)
	}
	runPelletCommand(t, application, "reopen", "shared-1")
	runPelletCommand(t, application, "release", "shared-2")
	startedNext := runPelletCommand(t, application, "start-next")
	if !strings.Contains(startedNext, `"selection_reason":"next_open"`) || !strings.Contains(startedNext, `"workspace":{"id":`+strconv.FormatInt(linkedWorkspaceID, 10)) {
		t.Fatalf("start-next output = %s", startedNext)
	}
	resumedNext := runPelletCommand(t, application, "start-next", "--external-id", "missing")
	if !strings.Contains(resumedNext, `"selection_reason":"resume_in_progress"`) {
		t.Fatalf("filtered start-next resume = %s", resumedNext)
	}
	var resumed struct {
		Data nextData `json:"data"`
	}
	if err := json.Unmarshal([]byte(resumedNext), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Data.Pellet == nil {
		t.Fatalf("resumed start-next has no pellet: %s", resumedNext)
	}
	runPelletCommand(t, application, "close", resumed.Data.Pellet.ID)
	exhausted := runPelletCommand(t, application, "start-next", "--external-id", "missing")
	if !strings.Contains(exhausted, `"selection_reason":"none","pellet":null`) {
		t.Fatalf("empty start-next = %s", exhausted)
	}

	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "pellet-command-unregistered", unregisteredWorkTree)
	current = unregisteredWorkTree
	stateBeforeUnregistered := capturePelletLogicalState(t, databasePath)
	stdout, stderr, exit = runTestApp(application, "next")
	if exit != 3 || stdout != "" || !strings.Contains(stderr, `"code":"workspace_not_registered"`) {
		t.Fatalf("unregistered next = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}
	if stateAfter := capturePelletLogicalState(t, databasePath); !reflect.DeepEqual(stateAfter, stateBeforeUnregistered) {
		t.Fatalf("unregistered next changed persistent state:\nbefore=%q\nafter=%q", stateBeforeUnregistered, stateAfter)
	}
}

func TestPelletLifecycleCommandsJSONGolden(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "lifecycle golden database")
	mainWorkTree := filepath.Join(common, "main")
	linkedWorkTree := filepath.Join(common, "linked")
	if err := os.MkdirAll(mainWorkTree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(mainWorkTree, "README"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, mainWorkTree, "add", "README")
	runGitTest(
		t, mainWorkTree,
		"-c", "user.name=Pellets Test", "-c", "user.email=pellets@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture",
	)
	runGitTest(t, mainWorkTree, "worktree", "add", "--quiet", "-b", "lifecycle-golden-linked", linkedWorkTree)
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := mainWorkTree
	application := projectTestApp(&current)
	mainProject := runProjectInit(t, application, "shared")
	current = linkedWorkTree
	linkedProject := runProjectInit(t, application, "shared")
	mainWorkspaceID := mainProject.Workspaces[0].ID
	linkedWorkspaceID := linkedProject.Workspaces[1].ID
	if mainWorkspaceID != 1 || linkedWorkspaceID != 2 {
		t.Fatalf("lifecycle fixture workspace IDs = main %d linked %d, want 1 and 2", mainWorkspaceID, linkedWorkspaceID)
	}

	for _, title := range []string{"primary lifecycle", "secondary lifecycle", "recovered lifecycle"} {
		runPelletCommand(t, application, "add", title)
	}
	databasePath := discovery.DatabasePath(common)
	var outputs strings.Builder

	current = mainWorkTree
	appendPelletGolden(t, &outputs, "start-success", runPelletCommand(t, application, "start", "shared-1"))
	appendPelletGolden(t, &outputs, "start-idempotent-resume", runPelletCommand(t, application, "start", "shared-1"))

	beforeWorkspaceConflict := capturePelletLogicalState(t, databasePath)
	appendPelletGolden(t, &outputs, "error-workspace-already-in-progress", runPelletError(t, application, 4, "start", "shared-2"))
	assertPelletLogicalState(t, databasePath, beforeWorkspaceConflict, "workspace_already_in_progress")

	current = linkedWorkTree
	beforeElsewhere := capturePelletLogicalState(t, databasePath)
	appendPelletGolden(t, &outputs, "error-pellet-in-progress-elsewhere", runPelletError(t, application, 4, "start", "shared-1"))
	appendPelletGolden(t, &outputs, "error-confirmation-required", runPelletError(
		t, application, 6, "release", "shared-1", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10),
	))
	appendPelletGolden(t, &outputs, "error-recovery-workspace-mismatch", runPelletError(
		t, application, 4, "release", "shared-1", "--recover-workspace", strconv.FormatInt(linkedWorkspaceID, 10), "--yes",
	))
	assertPelletLogicalState(t, databasePath, beforeElsewhere, "ownership and recovery errors")

	appendPelletGolden(t, &outputs, "release-recovered", runPelletCommand(
		t, application, "release", "shared-1", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10), "--yes",
	))
	runPelletCommand(t, application, "start", "shared-2")
	appendPelletGolden(t, &outputs, "release-owner", runPelletCommand(t, application, "release", "shared-2"))

	appendPelletGolden(t, &outputs, "start-next-open", runPelletCommand(t, application, "start-next"))
	beforeResume := capturePelletLogicalState(t, databasePath)
	appendPelletGolden(t, &outputs, "start-next-resume", runPelletCommand(t, application, "start-next", "--external-id", "missing"))
	assertPelletLogicalState(t, databasePath, beforeResume, "start-next resume")
	appendPelletGolden(t, &outputs, "close-owner", runPelletCommand(t, application, "close", "shared-1"))
	appendPelletGolden(t, &outputs, "reopen", runPelletCommand(t, application, "reopen", "shared-1"))
	beforeNone := capturePelletLogicalState(t, databasePath)
	appendPelletGolden(t, &outputs, "start-next-none", runPelletCommand(t, application, "start-next", "--external-id", "missing"))
	assertPelletLogicalState(t, databasePath, beforeNone, "empty start-next")

	runPelletCommand(t, application, "start", "shared-1")
	appendPelletGolden(t, &outputs, "defer-owner", runPelletCommand(t, application, "defer", "shared-1"))

	current = mainWorkTree
	runPelletCommand(t, application, "start", "shared-2")
	current = linkedWorkTree
	appendPelletGolden(t, &outputs, "close-recovered", runPelletCommand(
		t, application, "close", "shared-2", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10), "--yes",
	))
	current = mainWorkTree
	runPelletCommand(t, application, "start", "shared-3")
	current = linkedWorkTree
	appendPelletGolden(t, &outputs, "defer-recovered", runPelletCommand(
		t, application, "defer", "shared-3", "--recover-workspace", strconv.FormatInt(mainWorkspaceID, 10), "--yes",
	))

	runPelletCommand(t, application, "add", "retry lifecycle", "--external-id", "Retry:Exact")
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		CREATE TRIGGER lifecycle_golden_reject_start_next
		BEFORE UPDATE ON pellets
		WHEN OLD.status = 'open' AND NEW.status = 'in_progress'
		BEGIN
			SELECT RAISE(ABORT, 'forced lifecycle golden retry');
		END`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	beforeRetry := capturePelletLogicalState(t, databasePath)
	appendPelletGolden(t, &outputs, "error-start-next-conflict", runPelletError(
		t, application, 4, "start-next", "--external-id", "Retry:Exact",
	))
	assertPelletLogicalState(t, databasePath, beforeRetry, "start_next_conflict")
	database, err = sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("DROP TRIGGER lifecycle_golden_reject_start_next"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	beforeBusy := capturePelletLogicalState(t, databasePath)
	database, err = sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(context.Background())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		connection.Close()
		database.Close()
		t.Fatal(err)
	}
	appendPelletGolden(t, &outputs, "error-database-busy", runPelletError(
		t, application, 4, "start-next", "--external-id", "Retry:Exact",
	))
	if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertPelletLogicalState(t, databasePath, beforeBusy, "database_busy")

	assertGolden(t, "pellet-lifecycle.golden", outputs.String())
}

func TestPelletCommandUsageValidationIsStrictAndSideEffectFree(t *testing.T) {
	t.Parallel()

	application := New(
		"test",
		AddCommand(emptyPelletManager()), MoveCommand(emptyPelletManager()), ListCommand(emptyPelletManager()), ShowCommand(emptyPelletManager()),
		EditCommand(emptyPelletManager()), NextCommand(emptyPelletManager()), StartCommand(emptyPelletManager()),
		StartNextCommand(emptyPelletManager()), ReleaseCommand(emptyPelletManager()), CloseCommand(emptyPelletManager()),
		ReopenCommand(emptyPelletManager()), DeferCommand(emptyPelletManager()),
	)
	workingDirectoryCalls := 0
	application.workingDirectory = func() (string, error) {
		workingDirectoryCalls++
		return "", fmt.Errorf("usage validation crossed discovery boundary")
	}
	tests := []struct {
		args []string
		code string
	}{
		{[]string{"add"}, "missing_title"},
		{[]string{"add", "title", "--description", "one", "--description-file", "two"}, "conflicting_flags"},
		{[]string{"add", "title", "--before", "shared-1", "--after", "shared-2"}, "conflicting_flags"},
		{[]string{"add", "title", "--maybe-later", "--before", "shared-1"}, "conflicting_flags"},
		{[]string{"move"}, "missing_reference"},
		{[]string{"move", "shared-1"}, "missing_placement"},
		{[]string{"move", "shared-1", "--before", "shared-2", "--after", "shared-3"}, "conflicting_flags"},
		{[]string{"move", "shared-1", "--after", "shared-1"}, "invalid_move_target"},
		{[]string{"list", "--status", "unknown"}, "invalid_status"},
		{[]string{"list", "--status", "open", "--all"}, "conflicting_flags"},
		{[]string{"list", "--limit", "0"}, "invalid_limit"},
		{[]string{"show", "12"}, "invalid_reference"},
		{[]string{"edit", "shared-1"}, "missing_edit"},
		{[]string{"edit", "shared-1", "--status", "closed"}, "unknown_flag"},
		{[]string{"edit", "shared-1", "--external-id", "x", "--clear-external-id"}, "conflicting_flags"},
		{[]string{"next", "extra"}, "unexpected_argument"},
		{[]string{"start"}, "missing_reference"},
		{[]string{"start", "shared-1", "--yes"}, "unexpected_argument"},
		{[]string{"start-next", "extra"}, "unexpected_argument"},
		{[]string{"release", "shared-1", "--recover-workspace", "1"}, "confirmation_required"},
		{[]string{"close", "shared-1", "--yes"}, "recovery_workspace_required"},
		{[]string{"defer", "shared-1", "--recover-workspace", "01", "--yes"}, "invalid_workspace_id"},
		{[]string{"reopen", "shared-1", "extra"}, "unexpected_argument"},
	}
	for _, test := range tests {
		stdout, stderr, exit := runTestApp(application, test.args...)
		wantExit := 2
		if test.code == "confirmation_required" {
			wantExit = 6
		}
		if exit != wantExit || stdout != "" || !strings.Contains(stderr, `"code":"`+test.code+`"`) {
			t.Errorf("%v = exit %d stdout %q stderr %q, want exit %d code %s", test.args, exit, stdout, stderr, wantExit, test.code)
		}
	}
	if workingDirectoryCalls != 0 {
		t.Fatalf("invalid pellet commands crossed working-directory boundary %d times", workingDirectoryCalls)
	}
}

func TestDescriptionMayBeExplicitlyEditedToEmpty(t *testing.T) {
	t.Parallel()

	parsed, err := parseEdit([]string{"shared-1", "--description="})
	if err != nil {
		t.Fatal(err)
	}
	input := parsed.(editInput)
	if input.Description == nil || *input.Description != "" || input.DescriptionFile != nil {
		t.Fatalf("empty inline description parse = %#v", input)
	}
	parsed, err = parseAdd([]string{"title", "--description", ""})
	if err != nil {
		t.Fatal(err)
	}
	added := parsed.(addInput)
	if added.Description == nil || *added.Description != "" {
		t.Fatalf("empty add description parse = %#v", added)
	}
}

func emptyPelletManager() (manager app.PelletManager) { return manager }

func runPelletCommand(t *testing.T, application *App, arguments ...string) string {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, arguments...)
	if exit != 0 || stderr != "" {
		t.Fatalf("pl %s = exit %d stdout %q stderr %q", strings.Join(arguments, " "), exit, stdout, stderr)
	}
	return stdout
}

func runPelletError(t *testing.T, application *App, wantExit int, arguments ...string) string {
	t.Helper()
	stdout, stderr, exit := runTestApp(application, arguments...)
	if exit != wantExit || stdout != "" || stderr == "" {
		t.Fatalf("pl %s = exit %d stdout %q stderr %q, want exit %d with stderr only", strings.Join(arguments, " "), exit, stdout, stderr, wantExit)
	}
	return stderr
}

func appendPelletGolden(t *testing.T, output *strings.Builder, label, raw string) {
	t.Helper()
	if strings.Count(raw, "\n") != 1 || !strings.HasSuffix(raw, "\n") {
		t.Fatalf("%s output is not one compact JSON line: %q", label, raw)
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("decode %s JSON %q: %v", label, raw, err)
	}
	normalizePelletTimestamps(value)
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(output, "%s %s", label, encoded.String())
}

func assertPelletLogicalState(t *testing.T, path string, want []string, operation string) {
	t.Helper()
	if got := capturePelletLogicalState(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s changed persistent state:\nbefore=%q\nafter=%q", operation, want, got)
	}
}

func normalizePelletTimestamps(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if (key == "created_at" || key == "updated_at" || key == "completed_at") && child != nil {
				typed[key] = "<timestamp>"
				continue
			}
			normalizePelletTimestamps(child)
		}
	case []any:
		for _, child := range typed {
			normalizePelletTimestamps(child)
		}
	}
}

func capturePelletLogicalState(t *testing.T, path string) []string {
	t.Helper()
	database, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.Query(`
		SELECT value FROM (
			SELECT 'project|' || quote(project_id) || '|' || quote(code) || '|' || quote(next_pellet_number) || '|' || quote(updated_at) AS value, 0 AS kind, project_id AS first, 0 AS second FROM projects
			UNION ALL
			SELECT 'workspace|' || quote(workspace_id) || '|' || quote(project_id) || '|' || quote(root_path) || '|' || quote(git_dir) || '|' || quote(created_at) || '|' || quote(updated_at), 1, project_id, workspace_id FROM project_workspaces
			UNION ALL
			SELECT 'pellet|' || quote(rowid) || '|' || quote(project_id) || '|' || quote(workspace_id) || '|' || quote(number) || '|' || quote(title) || '|' || quote(description) || '|' || quote(external_id) || '|' || quote(group_id) || '|' || quote(status) || '|' || quote(priority) || '|' || quote(created_at) || '|' || quote(updated_at) || '|' || quote(completed_at), 2, project_id, number FROM pellets
		)
		ORDER BY kind, first, second`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	state := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		state = append(state, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return state
}
