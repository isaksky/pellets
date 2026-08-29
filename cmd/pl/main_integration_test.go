package main

import (
	"bytes"
	"context"
	"database/sql"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

func TestFoundationCompiledExecutable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("foundation integration tests require native Git: %v", err)
	}
	executable := buildFoundationExecutable(t)

	t.Run("process contract", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "process contract with spaces and 界")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}

		result := runFoundationCLIWithBlockedStdin(t, executable, root, "--version")
		assertFoundationResult(t, result, 0, "pl dev (JSON schema 1)\n", "")

		result = runFoundationCLI(t, executable, root, "unknown-foundation-command")
		assertFoundationResult(t, result, 2, "", foundationErrorJSON(
			"unknown_command",
			"unknown command \"unknown-foundation-command\"",
			map[string]any{"command": "unknown-foundation-command"},
		))

		result = runFoundationCLI(t, executable, root, "project", "list")
		assertFoundationResult(t, result, 3, "", foundationErrorJSON(
			"database_not_found",
			"no Pellets database was found in the current directory or its ancestors",
			map[string]any{"start_path": foundationCanonicalPath(t, root)},
		))

		metadataPath := filepath.Join(root, discovery.MetadataDirectory)
		if err := os.Mkdir(metadataPath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(discovery.DatabasePath(root), []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		result = runFoundationCLI(t, executable, root, "project", "list")
		assertFoundationResult(t, result, 5, "", foundationErrorJSON(
			"database_open_failed",
			"could not open database",
			nil,
		))
	})

	t.Run("compiled pellet queue workflow", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "compiled pellet queue 界")
		mainRoot := filepath.Join(common, "main")
		linkedRoot := filepath.Join(common, "linked")
		createFoundationRepository(t, mainRoot)
		if output, err := foundationGitCommand(
			mainRoot, "worktree", "add", "--quiet", "-b", "compiled-pellet-queue", linkedRoot,
		); err != nil {
			t.Fatalf("add pellet workflow linked worktree: %v\n%s", err, output)
		}

		decodeFoundationSuccess[foundationInitDB](
			t, runFoundationCLI(t, executable, common, "init-db"), "init-db",
		)
		mainProject := decodeFoundationSuccess[foundationProject](
			t, runFoundationCLI(t, executable, mainRoot, "init", "--code", "queue"), "init",
		)
		linkedProject := decodeFoundationSuccess[foundationProject](
			t, runFoundationCLI(t, executable, linkedRoot, "init", "--code", "queue"), "init",
		)
		if len(mainProject.Workspaces) != 1 || len(linkedProject.Workspaces) != 2 {
			t.Fatalf("compiled pellet workflow workspaces = %#v then %#v", mainProject, linkedProject)
		}

		first := decodeFoundationSuccess[foundationPellet](
			t,
			runFoundationCLI(
				t, executable, mainRoot,
				"add", "First compiled pellet", "--description", "from executable",
				"--external-id", "Case:Exact", "--group", "Rollout/A",
			),
			"add",
		)
		second := decodeFoundationSuccess[foundationPellet](
			t,
			runFoundationCLI(
				t, executable, linkedRoot,
				"--project", "queue", "add", "Second compiled pellet", "--after", "queue-1",
				"--external-id", "Case:Exact", "--group", "Rollout/A",
			),
			"add",
		)
		if first.ID != "queue-1" || first.Priority == nil || *first.Priority != 1024 || second.ID != "queue-2" || second.Priority == nil || *second.Priority != 2048 {
			t.Fatalf("compiled additions = %#v and %#v", first, second)
		}

		listed := decodeFoundationSuccess[[]foundationPellet](
			t,
			runFoundationCLI(t, executable, linkedRoot, "list", "--external-id", "Case:Exact", "--group", "Rollout/A"),
			"list",
		)
		if len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID {
			t.Fatalf("compiled list = %#v", listed)
		}
		movedBefore := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, linkedRoot, "move", second.ID, "--before", first.ID), "move",
		)
		listed = decodeFoundationSuccess[[]foundationPellet](
			t, runFoundationCLI(t, executable, linkedRoot, "list"), "list",
		)
		if movedBefore.ID != second.ID || len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != first.ID {
			t.Fatalf("compiled upward move = %#v; list = %#v", movedBefore, listed)
		}
		movedAfter := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "move", second.ID, "--after", first.ID), "move",
		)
		listed = decodeFoundationSuccess[[]foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "list"), "list",
		)
		if movedAfter.ID != second.ID || len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID {
			t.Fatalf("compiled downward move = %#v; list = %#v", movedAfter, listed)
		}
		next := decodeFoundationSuccess[foundationNext](
			t,
			runFoundationCLI(t, executable, linkedRoot, "next", "--external-id", "Case:Exact", "--group", "Rollout/A"),
			"next",
		)
		if next.SelectionReason != "next_open" || next.Pellet == nil || next.Pellet.ID != first.ID {
			t.Fatalf("compiled next = %#v", next)
		}
		startedNext := decodeFoundationSuccess[foundationNext](
			t,
			runFoundationCLI(t, executable, linkedRoot, "start-next", "--external-id", "Case:Exact", "--group", "Rollout/A"),
			"start-next",
		)
		linkedWorkspaceID := linkedProject.Workspaces[len(linkedProject.Workspaces)-1].ID
		if startedNext.SelectionReason != "next_open" || startedNext.Pellet == nil || startedNext.Pellet.ID != first.ID || startedNext.Pellet.Status != "in_progress" || startedNext.Pellet.Workspace == nil || startedNext.Pellet.Workspace.ID != linkedWorkspaceID {
			t.Fatalf("compiled start-next = %#v", startedNext)
		}
		resumed := decodeFoundationSuccess[foundationNext](
			t, runFoundationCLI(t, executable, linkedRoot, "start-next", "--external-id", "does-not-match"), "start-next",
		)
		if resumed.SelectionReason != "resume_in_progress" || resumed.Pellet == nil || !reflect.DeepEqual(resumed.Pellet, startedNext.Pellet) {
			t.Fatalf("compiled start-next resume = %#v, want %#v", resumed, startedNext)
		}
		mainStarted := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "start", second.ID), "start",
		)
		if mainStarted.Status != "in_progress" || mainStarted.Workspace == nil || mainStarted.Workspace.ID != mainProject.Workspaces[0].ID {
			t.Fatalf("compiled main-worktree start = %#v", mainStarted)
		}
		crossWorkspace := runFoundationCLI(t, executable, mainRoot, "close", first.ID)
		if crossWorkspace.exit != 4 || crossWorkspace.stdout != "" || !strings.Contains(crossWorkspace.stderr, `"code":"pellet_in_progress_elsewhere"`) {
			t.Fatalf("compiled cross-workspace close = %#v", crossWorkspace)
		}
		closedFirst := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, linkedRoot, "close", first.ID), "close",
		)
		closedSecond := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "close", second.ID), "close",
		)
		if closedFirst.Status != "closed" || closedFirst.Priority != nil || closedFirst.Workspace != nil || closedFirst.CompletedAt == nil || closedSecond.Status != "closed" || closedSecond.Priority != nil || closedSecond.Workspace != nil || closedSecond.CompletedAt == nil {
			t.Fatalf("compiled closes = %#v and %#v", closedFirst, closedSecond)
		}
		searchedClosed := decodeFoundationSuccess[[]foundationPellet](
			t,
			runFoundationCLI(
				t, executable, linkedRoot, "search", "Case:Exact",
				"--external-id", "Case:Exact", "--group", "Rollout/A",
			),
			"search",
		)
		if len(searchedClosed) != 2 || searchedClosed[0].Status != "closed" || searchedClosed[1].Status != "closed" {
			t.Fatalf("compiled default search did not include closed pellets: %#v", searchedClosed)
		}
		malformedLiteral := decodeFoundationSuccess[[]foundationPellet](
			t, runFoundationCLI(t, executable, linkedRoot, "search", `Case:Exact OR (`), "search",
		)
		if malformedLiteral == nil || len(malformedLiteral) != 0 {
			t.Fatalf("compiled malformed FTS text = %#v, want typed empty result", malformedLiteral)
		}

		edited := decodeFoundationSuccess[foundationPellet](
			t,
			runFoundationCLI(t, executable, linkedRoot, "edit", second.ID, "--title", "Second edited", "--clear-group"),
			"edit",
		)
		shown := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "show", second.ID), "show",
		)
		if edited.Title != "Second edited" || edited.Group != nil || !reflect.DeepEqual(shown, edited) {
			t.Fatalf("compiled edit/show = %#v / %#v", edited, shown)
		}
		searchedEdit := decodeFoundationSuccess[[]foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "search", "edited"), "search",
		)
		if len(searchedEdit) != 1 || searchedEdit[0].ID != second.ID || !reflect.DeepEqual(searchedEdit[0], edited) {
			t.Fatalf("compiled search after indexed edit = %#v, want %#v", searchedEdit, edited)
		}

		empty := decodeFoundationSuccess[[]foundationPellet](
			t, runFoundationCLI(t, executable, mainRoot, "list", "--external-id", "case:exact"), "list",
		)
		if empty == nil || len(empty) != 0 {
			t.Fatalf("compiled typed empty list = %#v", empty)
		}
		none := decodeFoundationSuccess[foundationNext](
			t, runFoundationCLI(t, executable, mainRoot, "next", "--external-id", "case:exact"), "next",
		)
		if none.SelectionReason != "none" || none.Pellet != nil {
			t.Fatalf("compiled typed empty next = %#v", none)
		}

		databasePath := discovery.DatabasePath(foundationCanonicalPath(t, common))
		database, err := sqlite.Open(context.Background(), databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("DROP TABLE pellets_fts"); err != nil {
			database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		ftsUnavailable := runFoundationCLI(t, executable, mainRoot, "search", "edited")
		if ftsUnavailable.exit != 5 || ftsUnavailable.stdout != "" || !strings.Contains(ftsUnavailable.stderr, `"code":"fts_unavailable"`) {
			t.Fatalf("compiled unavailable FTS search = %#v", ftsUnavailable)
		}
	})

	t.Run("compiled project memory workflow", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "compiled memory workflow 界")
		createFoundationRepository(t, root)
		decodeFoundationSuccess[foundationProject](
			t, runFoundationCLI(t, executable, root, "init", "--code", "memory"), "init",
		)

		agent := decodeFoundationSuccess[foundationMemory](
			t,
			runFoundationCLI(t, executable, root, "memory", "add", "--text", "memory-123 remains ordinary text"),
			"memory add",
		)
		if agent.ID != 1 || agent.Project != "memory" || agent.CreatedBy != "agent" || agent.HumanApproved || agent.ApprovedAt != nil || agent.CreatedAt != agent.UpdatedAt {
			t.Fatalf("compiled agent memory = %#v", agent)
		}
		assertFoundationTimestamp(t, agent.CreatedAt)

		inputPath := filepath.Join(root, "reviewed memory 世界.txt")
		if err := os.WriteFile(inputPath, []byte("human-reviewed compiled fact\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		human := decodeFoundationSuccess[foundationMemory](
			t,
			runFoundationCLI(t, executable, root, "memory", "add", "--file", inputPath, "--created-by", "human"),
			"memory add",
		)
		if human.ID != 2 || human.CreatedBy != "human" || !human.HumanApproved || human.ApprovedAt == nil || *human.ApprovedAt != human.CreatedAt || human.UpdatedAt != human.CreatedAt {
			t.Fatalf("compiled human memory = %#v", human)
		}

		listed := decodeFoundationSuccess[[]foundationMemory](
			t, runFoundationCLI(t, executable, root, "memory", "list"), "memory list",
		)
		if len(listed) != 2 || listed[0].ID != human.ID || listed[1].ID != agent.ID {
			t.Fatalf("compiled memory list = %#v", listed)
		}
		approvedOnly := decodeFoundationSuccess[[]foundationMemory](
			t,
			runFoundationCLI(t, executable, root, "memory", "list", "--approved-only", "--limit", "1"),
			"memory list",
		)
		if !reflect.DeepEqual(approvedOnly, []foundationMemory{human}) {
			t.Fatalf("compiled approved memory list = %#v, want %#v", approvedOnly, human)
		}
		shown := decodeFoundationSuccess[foundationMemory](
			t, runFoundationCLI(t, executable, root, "memory", "show", "1"), "memory show",
		)
		if !reflect.DeepEqual(shown, agent) {
			t.Fatalf("compiled shown memory = %#v, want %#v", shown, agent)
		}
		approved := decodeFoundationSuccess[foundationMemory](
			t, runFoundationCLI(t, executable, root, "memory", "approve", "1"), "memory approve",
		)
		if approved.ApprovedAt == nil || !approved.HumanApproved || approved.CreatedBy != "agent" || approved.CreatedAt != agent.CreatedAt || approved.Text != agent.Text || approved.UpdatedAt != *approved.ApprovedAt {
			t.Fatalf("compiled approved agent memory = %#v", approved)
		}
		repeated := decodeFoundationSuccess[foundationMemory](
			t, runFoundationCLI(t, executable, root, "memory", "approve", "1"), "memory approve",
		)
		if !reflect.DeepEqual(repeated, approved) {
			t.Fatalf("compiled repeated approval = %#v, want %#v", repeated, approved)
		}

		missing := runFoundationCLI(t, executable, root, "memory", "show", "999")
		if missing.exit != 3 || missing.stdout != "" || !strings.Contains(missing.stderr, `"code":"memory_not_found"`) {
			t.Fatalf("compiled missing memory show = %#v", missing)
		}
		notYetImplemented := runFoundationCLI(t, executable, root, "memory", "search", "compiled")
		if notYetImplemented.exit != 2 || notYetImplemented.stdout != "" || !strings.Contains(notYetImplemented.stderr, `"code":"unknown_subcommand"`) {
			t.Fatalf("compiled out-of-scope memory search = %#v", notYetImplemented)
		}

		database, err := sqlite.Open(context.Background(), discovery.DatabasePath(root))
		if err != nil {
			t.Fatal(err)
		}
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM memories", 2)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM memories_fts", 2)
		for _, forbidden := range []string{"number", "pellet_id", "external_id", "group_id", "status", "priority", "workspace_id"} {
			assertFoundationQueryInt(t, database, `
				SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name = ?`, 0, forbidden)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Git root initialization and immutable registration", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "foundation repository with spaces and 世界")
		createFoundationRepository(t, repository)
		nested := filepath.Join(repository, "nested working", "目录")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}

		headBefore := foundationGitText(t, repository, "rev-parse", "HEAD")
		indexBefore := foundationGitBytes(t, repository, "ls-files", "--stage", "-z")
		statusBefore := foundationGitBytes(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
		excludePath := foundationExcludePath(t, repository)
		excludeBefore := readFoundationFile(t, excludePath)

		result := runFoundationCLI(t, executable, nested, "init", "--code", "foundation")
		project := decodeFoundationSuccess[foundationProject](t, result, "init")
		if project.Code != "foundation" || project.GitCommonDir != ".git" || len(project.Workspaces) != 1 || project.Workspaces[0].RootPath != "." || project.CreatedAt != project.UpdatedAt {
			t.Fatalf("initialized project = %#v", project)
		}
		assertFoundationTimestamp(t, project.CreatedAt)

		canonicalRepository := foundationCanonicalPath(t, repository)
		databasePath := discovery.DatabasePath(canonicalRepository)
		if _, err := os.Stat(databasePath); err != nil {
			t.Fatalf("database was not placed at Git root %q: %v", databasePath, err)
		}
		if _, err := os.Stat(discovery.DatabasePath(nested)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database unexpectedly exists below Git root: %v", err)
		}
		assertFoundationDatabase(t, databasePath, 1)

		shown := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, nested, "project", "show"),
			"project show",
		)
		if !reflect.DeepEqual(shown, project) {
			t.Fatalf("current project = %#v, want %#v", shown, project)
		}

		repeatedResult := runFoundationCLI(t, executable, nested, "init", "--code=foundation")
		repeated := decodeFoundationSuccess[foundationProject](t, repeatedResult, "init")
		if !reflect.DeepEqual(repeated, project) || repeatedResult.stdout != result.stdout {
			t.Fatalf("idempotent registration = %#v, want unchanged %#v", repeated, project)
		}

		conflict := runFoundationCLI(t, executable, nested, "init", "--code", "changed")
		assertFoundationResult(t, conflict, 4, "", foundationErrorJSON(
			"project_repository_already_registered",
			"the Git repository is already registered with a different immutable code",
			map[string]any{
				"existing_code":  "foundation",
				"requested_code": "changed",
			},
		))

		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, repository, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(projects, []foundationProject{project}) {
			t.Fatalf("projects = %#v, want only %#v", projects, project)
		}

		assertFoundationGitState(t, repository, headBefore, indexBefore, statusBefore)
		assertFoundationPathAbsent(t, filepath.Join(repository, ".gitignore"))
		excludeAfter := readFoundationFile(t, excludePath)
		if want := appendFoundationExclude(excludeBefore); !bytes.Equal(excludeAfter, want) {
			t.Fatalf("local exclude = %q, want %q", excludeAfter, want)
		}
		for _, path := range discovery.DatabasePaths(databasePath) {
			relative, err := filepath.Rel(canonicalRepository, path)
			if err != nil {
				t.Fatal(err)
			}
			runFoundationGit(t, repository, "check-ignore", "--quiet", "--no-index", "--", filepath.ToSlash(relative))
		}
	})

	t.Run("tracked database rejection is read only", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "tracked database repository 界")
		createFoundationRepository(t, repository)
		databasePath := discovery.DatabasePath(foundationCanonicalPath(t, repository))
		if err := os.Mkdir(filepath.Dir(databasePath), 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := []byte("tracked database sentinel")
		if err := os.WriteFile(databasePath, sentinel, 0o600); err != nil {
			t.Fatal(err)
		}
		runFoundationGit(t, repository, "add", "-f", "--", filepath.ToSlash(filepath.Join(discovery.MetadataDirectory, discovery.DatabaseFilename)))

		headBefore := foundationGitText(t, repository, "rev-parse", "HEAD")
		indexBefore := foundationGitBytes(t, repository, "ls-files", "--stage", "-z")
		statusBefore := foundationGitBytes(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
		excludePath := foundationExcludePath(t, repository)
		excludeBefore := readFoundationFile(t, excludePath)

		result := runFoundationCLI(t, executable, repository, "init-db")
		assertFoundationResult(t, result, 4, "", foundationErrorJSON(
			"database_already_tracked",
			"the Pellets database path is already tracked by Git",
			map[string]any{"database_path": databasePath},
		))
		if got := readFoundationFile(t, databasePath); !bytes.Equal(got, sentinel) {
			t.Fatalf("tracked database changed from %q to %q", sentinel, got)
		}
		assertFoundationGitState(t, repository, headBefore, indexBefore, statusBefore)
		if excludeAfter := readFoundationFile(t, excludePath); !bytes.Equal(excludeAfter, excludeBefore) {
			t.Fatalf("local exclude changed from %q to %q", excludeBefore, excludeAfter)
		}
		assertFoundationPathAbsent(t, filepath.Join(repository, ".gitignore"))
	})

	t.Run("nearest nested database wins", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "nearest database root 界")
		repository := filepath.Join(common, "outer repository with spaces")
		createFoundationRepository(t, repository)

		outerDatabase := discovery.DatabasePath(foundationCanonicalPath(t, common))
		decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		outerProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, repository, "init", "--code", "outer"),
			"init",
		)

		innerRoot := filepath.Join(repository, "nested database root 深")
		deep := filepath.Join(innerRoot, "deeper working directory")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		innerDatabase := discovery.DatabasePath(foundationCanonicalPath(t, innerRoot))
		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, innerRoot, "init-db"),
			"init-db",
		)
		if initialized.DatabasePath != innerDatabase {
			t.Fatalf("nested database path = %q, want %q", initialized.DatabasePath, innerDatabase)
		}

		innerProjects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, deep, "project", "list"),
			"project list",
		)
		if innerProjects == nil || len(innerProjects) != 0 {
			t.Fatalf("nearest nested project list = %#v, want []", innerProjects)
		}
		outerProjects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, repository, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(outerProjects, []foundationProject{outerProject}) {
			t.Fatalf("outer project list = %#v, want %#v", outerProjects, []foundationProject{outerProject})
		}
		assertFoundationDatabase(t, outerDatabase, 1)
		assertFoundationDatabase(t, innerDatabase, 0)
	})

	t.Run("sibling repositories share one database", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "sibling database root 共通")
		firstRoot := filepath.Join(common, "service alpha")
		secondRoot := filepath.Join(common, "service βeta")
		duplicateRoot := filepath.Join(common, "service duplicate")
		for _, root := range []string{firstRoot, secondRoot, duplicateRoot} {
			createFoundationRepository(t, root)
		}

		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		if initialized.DatabasePath != discovery.DatabasePath(foundationCanonicalPath(t, common)) {
			t.Fatalf("common database path = %q", initialized.DatabasePath)
		}
		first := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, firstRoot, "init", "--code", "svc-a"),
			"init",
		)
		second := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, secondRoot, "init", "--code", "svc-b"),
			"init",
		)
		if first.Workspaces[0].RootPath != "service alpha" || second.Workspaces[0].RootPath != "service βeta" {
			t.Fatalf("sibling paths = %q and %q", first.Workspaces[0].RootPath, second.Workspaces[0].RootPath)
		}

		conflict := runFoundationCLI(t, executable, duplicateRoot, "init", "--code", "svc-a")
		assertFoundationResult(t, conflict, 4, "", foundationErrorJSON(
			"project_code_already_registered",
			"the project code is already registered for a different Git repository",
			map[string]any{
				"code": "svc-a",
			},
		))

		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, common, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(projects, []foundationProject{first, second}) {
			t.Fatalf("sibling projects = %#v, want %#v", projects, []foundationProject{first, second})
		}
		assertFoundationDatabase(t, initialized.DatabasePath, 2)
	})

	t.Run("linked worktrees share one logical project", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "worktree database root 界")
		mainRoot := filepath.Join(common, "main work tree")
		linkedRoot := filepath.Join(common, "linked 工作 tree")
		secondLinkedRoot := filepath.Join(common, "second linked tree")
		createFoundationRepository(t, mainRoot)
		if _, err := foundationGitCommand(mainRoot, "worktree", "list", "--porcelain"); err != nil {
			t.Skipf("Git worktrees are unavailable: %v", err)
		}
		if output, err := foundationGitCommand(
			mainRoot,
			"worktree", "add", "--quiet", "-b", "pellets-foundation-linked", linkedRoot,
		); err != nil {
			t.Fatalf("add linked worktree: %v\n%s", err, output)
		}
		if output, err := foundationGitCommand(
			mainRoot,
			"worktree", "add", "--quiet", "-b", "pellets-foundation-linked-two", secondLinkedRoot,
		); err != nil {
			t.Fatalf("add second linked worktree: %v\n%s", err, output)
		}

		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		mainProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, mainRoot, "init", "--code", "worktree"),
			"init",
		)
		linkedProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, linkedRoot, "init", "--code", "worktree"),
			"init",
		)
		allWorkspaces := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, secondLinkedRoot, "init", "--code", "worktree"),
			"init",
		)
		if mainProject.Code != linkedProject.Code || linkedProject.Code != allWorkspaces.Code || len(allWorkspaces.Workspaces) != 3 {
			t.Fatalf("worktree projects = %#v %#v %#v", mainProject, linkedProject, allWorkspaces)
		}

		shown := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, secondLinkedRoot, "project", "show"),
			"project show",
		)
		if !reflect.DeepEqual(shown, allWorkspaces) {
			t.Fatalf("linked current project = %#v, want %#v", shown, allWorkspaces)
		}
		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, common, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(projects, []foundationProject{allWorkspaces}) {
			t.Fatalf("worktree projects = %#v", projects)
		}
		assertFoundationDatabase(t, initialized.DatabasePath, 1)
		database, err := sqlite.Open(context.Background(), initialized.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM project_workspaces", 3)
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("worktree lifecycle reconciliation is safe", func(t *testing.T) {
		common := filepath.Join(t.TempDir(), "compiled worktree lifecycle root 界")
		mainRoot := filepath.Join(common, "main")
		linkedRoot := filepath.Join(common, "linked original")
		movedRoot := filepath.Join(common, "linked moved 界")
		duplicateRoot := filepath.Join(common, "linked duplicate")
		replacementRoot := filepath.Join(common, "replacement")
		createFoundationRepository(t, mainRoot)
		if _, err := foundationGitCommand(mainRoot, "worktree", "list", "--porcelain"); err != nil {
			t.Skipf("Git worktrees are unavailable: %v", err)
		}
		if output, err := foundationGitCommand(
			mainRoot,
			"worktree", "add", "--quiet", "-b", "pellets-foundation-lifecycle", linkedRoot,
		); err != nil {
			t.Fatalf("add lifecycle worktree: %v\n%s", err, output)
		}

		mainGitBefore := captureFoundationGitState(t, mainRoot)
		linkedGitBefore := captureFoundationGitState(t, linkedRoot)
		excludePath := foundationExcludePath(t, mainRoot)
		excludeBefore := readFoundationFile(t, excludePath)

		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runFoundationCLI(t, executable, common, "init-db"),
			"init-db",
		)
		mainProject := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, mainRoot, "init", "--code", "shared"),
			"init",
		)
		registered := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, linkedRoot, "init", "--code", "shared"),
			"init",
		)
		if len(mainProject.Workspaces) != 1 || len(registered.Workspaces) != 2 {
			t.Fatalf("registered lifecycle project = %#v then %#v", mainProject, registered)
		}
		if !reflect.DeepEqual(registered.Workspaces[0], mainProject.Workspaces[0]) {
			t.Fatalf("main workspace changed while attaching linked worktree: %#v then %#v", mainProject.Workspaces[0], registered.Workspaces[0])
		}
		linkedWorkspace := registered.Workspaces[1]
		if linkedWorkspace.RootPath != "linked original" || !linkedWorkspace.RootPathRelative || linkedWorkspace.ID == mainProject.Workspaces[0].ID {
			t.Fatalf("linked workspace = %#v", linkedWorkspace)
		}
		assertFoundationTimestamp(t, linkedWorkspace.CreatedAt)
		assertFoundationTimestamp(t, linkedWorkspace.UpdatedAt)
		assertFoundationDatabase(t, initialized.DatabasePath, 1)
		assertFoundationGitStateMatches(t, mainRoot, mainGitBefore)
		assertFoundationGitStateMatches(t, linkedRoot, linkedGitBefore)
		if got := readFoundationFile(t, excludePath); !bytes.Equal(got, excludeBefore) {
			t.Fatalf("common-parent initialization changed local exclude from %q to %q", excludeBefore, got)
		}

		if err := os.CopyFS(duplicateRoot, os.DirFS(linkedRoot)); err != nil {
			t.Fatal(err)
		}
		duplicateGitBefore := captureFoundationGitState(t, duplicateRoot)
		databaseBeforeConflict := captureFoundationDatabaseState(t, initialized.DatabasePath)
		listBeforeConflict := runFoundationCLI(t, executable, common, "project", "list")
		projectsBeforeConflict := decodeFoundationSuccess[[]foundationProject](t, listBeforeConflict, "project list")
		if !reflect.DeepEqual(projectsBeforeConflict, []foundationProject{registered}) {
			t.Fatalf("projects before duplicate conflict = %#v, want %#v", projectsBeforeConflict, []foundationProject{registered})
		}

		conflict := runFoundationCLI(t, executable, duplicateRoot, "init", "--code", "shared")
		assertFoundationResult(t, conflict, 4, "", foundationErrorJSON(
			"workspace_identity_conflict",
			"the Git worktree root and Git directory do not identify one available workspace",
			map[string]any{
				"requested_root_path":  "linked duplicate",
				"requested_git_dir":    linkedWorkspace.GitDir,
				"git_dir_workspace_id": linkedWorkspace.ID,
			},
		))
		assertFoundationDatabaseState(t, initialized.DatabasePath, databaseBeforeConflict)
		listAfterConflict := runFoundationCLI(t, executable, common, "project", "list")
		if listAfterConflict != listBeforeConflict {
			t.Fatalf("project list changed after duplicate conflict from %#v to %#v", listBeforeConflict, listAfterConflict)
		}
		assertFoundationGitStateMatches(t, mainRoot, mainGitBefore)
		assertFoundationGitStateMatches(t, linkedRoot, linkedGitBefore)
		assertFoundationGitStateMatches(t, duplicateRoot, duplicateGitBefore)
		if got := readFoundationFile(t, excludePath); !bytes.Equal(got, excludeBefore) {
			t.Fatalf("duplicate conflict changed local exclude from %q to %q", excludeBefore, got)
		}

		runFoundationGit(t, mainRoot, "worktree", "move", linkedRoot, movedRoot)
		assertFoundationPathAbsent(t, linkedRoot)
		mainGitBeforeMoveReconciliation := captureFoundationGitState(t, mainRoot)
		movedGitBeforeReconciliation := captureFoundationGitState(t, movedRoot)
		moved := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, movedRoot, "init", "--code", "shared"),
			"init",
		)
		if moved.Code != registered.Code || moved.GitCommonDir != registered.GitCommonDir || moved.GitCommonDirRelative != registered.GitCommonDirRelative || moved.CreatedAt != registered.CreatedAt {
			t.Fatalf("moved project identity = %#v, want identity from %#v", moved, registered)
		}
		if len(moved.Workspaces) != 2 || !reflect.DeepEqual(moved.Workspaces[0], registered.Workspaces[0]) {
			t.Fatalf("workspaces after move = %#v, want unchanged main plus moved linked workspace", moved.Workspaces)
		}
		movedWorkspace := moved.Workspaces[1]
		if movedWorkspace.ID != linkedWorkspace.ID || movedWorkspace.RootPath != "linked moved 界" || !movedWorkspace.RootPathRelative || movedWorkspace.GitDir != linkedWorkspace.GitDir || movedWorkspace.GitDirRelative != linkedWorkspace.GitDirRelative || movedWorkspace.CreatedAt != linkedWorkspace.CreatedAt {
			t.Fatalf("moved workspace = %#v, want identity from %#v", movedWorkspace, linkedWorkspace)
		}
		assertFoundationTimestamp(t, movedWorkspace.UpdatedAt)
		shownMoved := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, movedRoot, "project", "show"),
			"project show",
		)
		if !reflect.DeepEqual(shownMoved, moved) {
			t.Fatalf("project shown from moved workspace = %#v, want %#v", shownMoved, moved)
		}
		assertFoundationGitStateMatches(t, mainRoot, mainGitBeforeMoveReconciliation)
		assertFoundationGitStateMatches(t, movedRoot, movedGitBeforeReconciliation)
		if got := readFoundationFile(t, excludePath); !bytes.Equal(got, excludeBefore) {
			t.Fatalf("move reconciliation changed local exclude from %q to %q", excludeBefore, got)
		}

		runFoundationGit(t, mainRoot, "worktree", "remove", movedRoot)
		assertFoundationPathAbsent(t, movedRoot)
		databaseBeforeStaleRead := captureFoundationDatabaseState(t, initialized.DatabasePath)
		staleProjects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, common, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(staleProjects, []foundationProject{moved}) {
			t.Fatalf("removed workspace was not retained as stale: %#v, want %#v", staleProjects, []foundationProject{moved})
		}
		assertFoundationDatabaseState(t, initialized.DatabasePath, databaseBeforeStaleRead)

		if output, err := foundationGitCommand(
			mainRoot,
			"worktree", "add", "--quiet", "-b", "pellets-foundation-replacement", replacementRoot,
		); err != nil {
			t.Fatalf("add replacement worktree: %v\n%s", err, output)
		}
		mainGitBeforeReplacement := captureFoundationGitState(t, mainRoot)
		replacementGitBefore := captureFoundationGitState(t, replacementRoot)
		withReplacement := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, replacementRoot, "init", "--code", "shared"),
			"init",
		)
		if withReplacement.Code != moved.Code || withReplacement.GitCommonDir != moved.GitCommonDir || withReplacement.GitCommonDirRelative != moved.GitCommonDirRelative || withReplacement.CreatedAt != moved.CreatedAt {
			t.Fatalf("replacement project identity = %#v, want identity from %#v", withReplacement, moved)
		}
		if len(withReplacement.Workspaces) != 3 || !reflect.DeepEqual(withReplacement.Workspaces[0], moved.Workspaces[0]) || !reflect.DeepEqual(withReplacement.Workspaces[1], movedWorkspace) {
			t.Fatalf("replacement silently changed or removed a stale workspace: %#v", withReplacement.Workspaces)
		}
		replacementWorkspace := withReplacement.Workspaces[2]
		if replacementWorkspace.ID == movedWorkspace.ID || replacementWorkspace.RootPath != "replacement" || !replacementWorkspace.RootPathRelative || replacementWorkspace.GitDir == movedWorkspace.GitDir {
			t.Fatalf("replacement workspace reused stale identity: replacement %#v, stale %#v", replacementWorkspace, movedWorkspace)
		}
		assertFoundationTimestamp(t, replacementWorkspace.CreatedAt)
		assertFoundationTimestamp(t, replacementWorkspace.UpdatedAt)
		shownReplacement := decodeFoundationSuccess[foundationProject](
			t,
			runFoundationCLI(t, executable, replacementRoot, "project", "show"),
			"project show",
		)
		if !reflect.DeepEqual(shownReplacement, withReplacement) {
			t.Fatalf("project shown from replacement workspace = %#v, want %#v", shownReplacement, withReplacement)
		}
		finalProjects := decodeFoundationSuccess[[]foundationProject](
			t,
			runFoundationCLI(t, executable, common, "project", "list"),
			"project list",
		)
		if !reflect.DeepEqual(finalProjects, []foundationProject{withReplacement}) {
			t.Fatalf("final lifecycle projects = %#v, want %#v", finalProjects, []foundationProject{withReplacement})
		}

		assertFoundationDatabase(t, initialized.DatabasePath, 1)
		database, err := sqlite.Open(context.Background(), initialized.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		assertFoundationQueryInt(t, database, "PRAGMA user_version", 3)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM project_workspaces", 3)
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		assertFoundationGitStateMatches(t, mainRoot, mainGitBefore)
		assertFoundationGitStateMatches(t, mainRoot, mainGitBeforeReplacement)
		assertFoundationGitStateMatches(t, replacementRoot, replacementGitBefore)
		if got := readFoundationFile(t, excludePath); !bytes.Equal(got, excludeBefore) {
			t.Fatalf("replacement registration changed local exclude from %q to %q", excludeBefore, got)
		}
		assertFoundationPathAbsent(t, filepath.Join(mainRoot, ".gitignore"))
		assertFoundationPathAbsent(t, filepath.Join(replacementRoot, ".gitignore"))
	})
}

type foundationResult struct {
	stdout string
	stderr string
	exit   int
}

type foundationSuccess[T any] struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Data          T      `json:"data"`
}

type foundationInitDB struct {
	DatabasePath string `json:"database_path"`
}

type foundationProject struct {
	Code                 string                `json:"code"`
	GitCommonDir         string                `json:"git_common_dir"`
	GitCommonDirRelative bool                  `json:"git_common_dir_relative"`
	Workspaces           []foundationWorkspace `json:"workspaces"`
	CreatedAt            string                `json:"created_at"`
	UpdatedAt            string                `json:"updated_at"`
}

type foundationWorkspace struct {
	ID               int64  `json:"id"`
	RootPath         string `json:"root_path"`
	RootPathRelative bool   `json:"root_path_relative"`
	GitDir           string `json:"git_dir"`
	GitDirRelative   bool   `json:"git_dir_relative"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type foundationPellet struct {
	ID          string                     `json:"id"`
	Project     string                     `json:"project"`
	Number      int64                      `json:"number"`
	Title       string                     `json:"title"`
	Description string                     `json:"description"`
	ExternalID  *string                    `json:"external_id"`
	Group       *string                    `json:"group"`
	Status      string                     `json:"status"`
	Priority    *int64                     `json:"priority"`
	Workspace   *foundationPelletWorkspace `json:"workspace"`
	CreatedAt   string                     `json:"created_at"`
	UpdatedAt   string                     `json:"updated_at"`
	CompletedAt *string                    `json:"completed_at"`
}

type foundationPelletWorkspace struct {
	ID               int64  `json:"id"`
	RootPath         string `json:"root_path"`
	RootPathRelative bool   `json:"root_path_relative"`
	GitDir           string `json:"git_dir"`
	GitDirRelative   bool   `json:"git_dir_relative"`
}

type foundationNext struct {
	SelectionReason string            `json:"selection_reason"`
	Pellet          *foundationPellet `json:"pellet"`
}

type foundationMemory struct {
	ID            int64   `json:"id"`
	Project       string  `json:"project"`
	Text          string  `json:"text"`
	CreatedBy     string  `json:"created_by"`
	HumanApproved bool    `json:"human_approved"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
	ApprovedAt    *string `json:"approved_at"`
}

type foundationError struct {
	SchemaVersion int `json:"schema_version"`
	Error         struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
}

type foundationGitState struct {
	head   string
	index  []byte
	status []byte
}

type foundationDatabaseState struct {
	userVersion int
	tables      map[string][][]string
}

func buildFoundationExecutable(t *testing.T) string {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	name := "pl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(t.TempDir(), name)
	command := exec.Command("go", "build", "-trimpath", "-o", executable, "./cmd/pl")
	command.Dir = repositoryRoot
	command.Env = append(
		os.Environ(),
		"CGO_ENABLED=0",
		"GOOS="+runtime.GOOS,
		"GOARCH="+runtime.GOARCH,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build foundation executable: %v\n%s", err, output)
	}

	info, err := buildinfo.ReadFile(executable)
	if err != nil {
		t.Fatalf("read foundation executable metadata: %v", err)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	for key, want := range map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        runtime.GOOS,
		"GOARCH":      runtime.GOARCH,
	} {
		if got := settings[key]; got != want {
			t.Fatalf("compiled executable %s = %q, want %q", key, got, want)
		}
	}
	return executable
}

func runFoundationCLI(t *testing.T, executable, directory string, args ...string) foundationResult {
	t.Helper()
	command, stdout, stderr := foundationCLICommand(executable, directory, args...)
	err := command.Run()
	return foundationProcessResult(t, stdout, stderr, err)
}

func runFoundationCLIWithBlockedStdin(t *testing.T, executable, directory string, args ...string) foundationResult {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	command, stdout, stderr := foundationCLICommand(executable, directory, args...)
	command.Stdin = reader
	if err := command.Start(); err != nil {
		t.Fatalf("start compiled CLI: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return foundationProcessResult(t, stdout, stderr, err)
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatal("compiled CLI read stdin without an explicit stdin option")
		return foundationResult{}
	}
}

func foundationCLICommand(executable, directory string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	command := exec.Command(executable, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	return command, &stdout, &stderr
}

func foundationProcessResult(t *testing.T, stdout, stderr *bytes.Buffer, err error) foundationResult {
	t.Helper()
	if err == nil {
		return foundationResult{stdout: stdout.String(), stderr: stderr.String()}
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run compiled CLI: %v", err)
	}
	return foundationResult{stdout: stdout.String(), stderr: stderr.String(), exit: exitError.ExitCode()}
}

func assertFoundationResult(t *testing.T, got foundationResult, exit int, stdout, stderr string) {
	t.Helper()
	if got.exit != exit || got.stdout != stdout || got.stderr != stderr {
		t.Fatalf(
			"compiled CLI = exit %d stdout %q stderr %q, want exit %d stdout %q stderr %q",
			got.exit,
			got.stdout,
			got.stderr,
			exit,
			stdout,
			stderr,
		)
	}
}

func decodeFoundationSuccess[T any](t *testing.T, result foundationResult, command string) T {
	t.Helper()
	if result.exit != 0 || result.stderr != "" {
		t.Fatalf("%s = exit %d stdout %q stderr %q", command, result.exit, result.stdout, result.stderr)
	}
	var envelope foundationSuccess[T]
	if err := json.Unmarshal([]byte(result.stdout), &envelope); err != nil {
		t.Fatalf("decode %s output %q: %v", command, result.stdout, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != command {
		t.Fatalf("%s envelope = %#v", command, envelope)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if want := string(encoded) + "\n"; result.stdout != want {
		t.Fatalf("%s stdout = %q, want exact compact envelope %q", command, result.stdout, want)
	}
	return envelope.Data
}

func foundationErrorJSON(code, message string, details map[string]any) string {
	var envelope foundationError
	envelope.SchemaVersion = 1
	envelope.Error.Code = code
	envelope.Error.Message = message
	envelope.Error.Details = details
	encoded, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func createFoundationRepository(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	runFoundationGit(t, root, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("foundation fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFoundationGit(t, root, "add", "tracked.txt")
	runFoundationGit(
		t,
		root,
		"-c", "user.name=Pellets Foundation Test",
		"-c", "user.email=pellets@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", "foundation fixture",
	)
}

func runFoundationGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	if output, err := foundationGitCommand(directory, args...); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func foundationGitBytes(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	output, err := foundationGitCommand(directory, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func foundationGitText(t *testing.T, directory string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(foundationGitBytes(t, directory, args...)))
}

func foundationGitCommand(directory string, args ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return command.CombinedOutput()
}

func captureFoundationGitState(t *testing.T, root string) foundationGitState {
	t.Helper()
	return foundationGitState{
		head:   foundationGitText(t, root, "rev-parse", "HEAD"),
		index:  foundationGitBytes(t, root, "ls-files", "--stage", "-z"),
		status: foundationGitBytes(t, root, "status", "--porcelain=v1", "--untracked-files=all"),
	}
}

func assertFoundationGitStateMatches(t *testing.T, root string, want foundationGitState) {
	t.Helper()
	assertFoundationGitState(t, root, want.head, want.index, want.status)
}

func foundationExcludePath(t *testing.T, root string) string {
	t.Helper()
	path := foundationGitText(t, root, "rev-parse", "--git-path", "info/exclude")
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(path))
	}
	return filepath.Clean(path)
}

func appendFoundationExclude(before []byte) []byte {
	want := append([]byte(nil), before...)
	if len(want) > 0 && want[len(want)-1] != '\n' {
		want = append(want, '\n')
	}
	return append(want, []byte(".pellets/\n")...)
}

func assertFoundationGitState(t *testing.T, root, head string, index, status []byte) {
	t.Helper()
	if got := foundationGitText(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("Git HEAD changed from %q to %q", head, got)
	}
	if got := foundationGitBytes(t, root, "ls-files", "--stage", "-z"); !bytes.Equal(got, index) {
		t.Fatalf("Git index changed from %q to %q", index, got)
	}
	if got := foundationGitBytes(t, root, "status", "--porcelain=v1", "--untracked-files=all"); !bytes.Equal(got, status) {
		t.Fatalf("Git status changed from %q to %q", status, got)
	}
}

func assertFoundationDatabase(t *testing.T, databasePath string, projectCount int) {
	t.Helper()
	assertFoundationMetadataEntries(t, databasePath)
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}

	assertFoundationQueryInt(t, database, "PRAGMA user_version", sqlite.LatestSchemaVersion)
	assertFoundationQueryInt(t, database, "PRAGMA foreign_keys", 1)
	assertFoundationQueryInt(t, database, "PRAGMA trusted_schema", 0)
	assertFoundationQueryInt(t, database, "PRAGMA synchronous", 2)
	assertFoundationQueryInt(t, database, "PRAGMA busy_timeout", 5000)
	assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM projects", projectCount)
	assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM application_metadata", 3)
	assertFoundationQueryInt(t, database, `
		SELECT COUNT(*)
		FROM sqlite_schema
		WHERE type = 'table' AND lower(name) LIKE '%migration%'`, 0)
	assertFoundationQueryInt(t, database, `
		SELECT COUNT(*)
		FROM application_metadata
		WHERE lower(key) LIKE '%version%'`, 0)

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		database.Close()
		t.Fatalf("journal_mode = %q, want WAL", journalMode)
	}
	var ftsSource string
	if err := database.QueryRow("SELECT fts5_source_id()").Scan(&ftsSource); err != nil || ftsSource == "" {
		database.Close()
		t.Fatalf("FTS5 capability = %q, %v", ftsSource, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertFoundationMetadataEntries(t, databasePath)
}

func captureFoundationDatabaseState(t *testing.T, databasePath string) foundationDatabaseState {
	t.Helper()
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	var state foundationDatabaseState
	if err := database.QueryRow("PRAGMA user_version").Scan(&state.userVersion); err != nil {
		t.Fatal(err)
	}
	state.tables = make(map[string][][]string)
	for _, table := range []struct {
		name, order string
	}{
		{name: "application_metadata", order: "rowid"},
		{name: "projects", order: "rowid"},
		{name: "project_workspaces", order: "rowid"},
		{name: "pellets", order: "rowid"},
		{name: "pellets_fts", order: "rowid"},
		{name: "pellets_fts_config", order: "k"},
		{name: "pellets_fts_data", order: "id"},
		{name: "pellets_fts_docsize", order: "id"},
		{name: "pellets_fts_idx", order: "segid, term"},
		{name: "memories", order: "rowid"},
		{name: "memories_fts", order: "rowid"},
		{name: "memories_fts_config", order: "k"},
		{name: "memories_fts_data", order: "id"},
		{name: "memories_fts_docsize", order: "id"},
		{name: "memories_fts_idx", order: "segid, term"},
		{name: "sqlite_sequence", order: "rowid"},
	} {
		rows, err := database.Query("SELECT * FROM " + table.name + " ORDER BY " + table.order)
		if err != nil {
			t.Fatalf("snapshot %s: %v", table.name, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("snapshot %s columns: %v", table.name, err)
		}
		for rows.Next() {
			raw := make([]sql.RawBytes, len(columns))
			destinations := make([]any, len(columns))
			for index := range raw {
				destinations[index] = &raw[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatalf("snapshot %s row: %v", table, err)
			}
			record := make([]string, len(raw))
			for index, value := range raw {
				if value == nil {
					record[index] = "null"
					continue
				}
				encoded, err := json.Marshal(string(value))
				if err != nil {
					rows.Close()
					t.Fatal(err)
				}
				record[index] = string(encoded)
			}
			state.tables[table.name] = append(state.tables[table.name], record)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close %s snapshot: %v", table.name, err)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("snapshot %s: %v", table.name, err)
		}
	}
	return state
}

func assertFoundationDatabaseState(t *testing.T, databasePath string, want foundationDatabaseState) {
	t.Helper()
	got := captureFoundationDatabaseState(t, databasePath)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("database state changed:\n got: %#v\nwant: %#v", got, want)
	}
}

func assertFoundationQueryInt(t *testing.T, database *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := database.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query result = %d, want %d\n%s", got, want, query)
	}
}

func assertFoundationMetadataEntries(t *testing.T, databasePath string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != discovery.DatabaseFilename || entries[0].IsDir() {
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		t.Fatalf("database metadata entries = %v, want only %s", names, discovery.DatabaseFilename)
	}
}

func assertFoundationTimestamp(t *testing.T, value string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || !strings.HasSuffix(value, "Z") {
		t.Fatalf("timestamp %q is not UTC RFC 3339: %v", value, err)
	}
}

func readFoundationFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func assertFoundationPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q exists or could not be inspected: %v", path, err)
	}
}

func foundationCanonicalPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(absolute)
}
