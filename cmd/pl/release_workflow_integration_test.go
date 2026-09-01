package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

func TestReleaseEndToEndCompiledWorkflows(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("release workflow requires native Git: %v", err)
	}
	executable := buildFoundationExecutable(t)

	t.Run("first use from a nested directory places the database at the Git root", func(t *testing.T) {
		repository := filepath.Join(t.TempDir(), "Git root placement with spaces 界")
		createFoundationRepository(t, repository)
		nested := filepath.Join(repository, "nested", "deep")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		initialized := decodeFoundationSuccess[foundationProject](
			t,
			runReleaseCLI(t, executable, nested, nil, "project", "show"),
			"project show",
		)
		if initialized.Code == "" || len(initialized.Workspaces) != 1 || initialized.Workspaces[0].RootPath != "." {
			t.Fatalf("Git-root bootstrap = %#v", initialized)
		}
		wantDatabase := discovery.DatabasePath(foundationCanonicalPath(t, repository))
		if _, err := os.Stat(wantDatabase); err != nil {
			t.Fatalf("Git-root database %q: %v", wantDatabase, err)
		}
		if _, err := os.Stat(discovery.DatabasePath(nested)); !os.IsNotExist(err) {
			t.Fatalf("nested invocation created a database at %q: %v", discovery.DatabasePath(nested), err)
		}
	})

	t.Run("common parent multi-project queue memory purge and recovery", func(t *testing.T) {
		common := filepath.Join(foundationShortTempDir(t), "release common parent with spaces 世界")
		mainRoot := filepath.Join(common, "shared")
		linkedA := filepath.Join(common, "linked alpha ü")
		linkedB := filepath.Join(common, "linked beta 界")
		otherRoot := filepath.Join(common, "other")
		createFoundationRepository(t, mainRoot)
		if output, err := foundationGitCommand(mainRoot, "worktree", "add", "--quiet", "-b", "release-linked-a", linkedA); err != nil {
			t.Fatalf("add first release worktree: %v\n%s", err, output)
		}
		if output, err := foundationGitCommand(mainRoot, "worktree", "add", "--quiet", "-b", "release-linked-b", linkedB); err != nil {
			t.Fatalf("add second release worktree: %v\n%s", err, output)
		}
		createFoundationRepository(t, otherRoot)

		nested := make(map[string]string)
		for _, root := range []string{mainRoot, linkedA, linkedB, otherRoot} {
			nested[root] = filepath.Join(root, "nested command directory")
			if err := os.MkdirAll(nested[root], 0o755); err != nil {
				t.Fatal(err)
			}
		}

		initialized := decodeFoundationSuccess[foundationInitDB](
			t,
			runReleaseCLI(t, executable, common, nil, "init-db"),
			"init-db",
		)
		wantDatabase := discovery.DatabasePath(foundationCanonicalPath(t, common))
		assertFoundationSamePath(t, initialized.DatabasePath, wantDatabase)

		mainProject := decodeFoundationSuccess[foundationProject](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "project", "show"),
			"project show",
		)
		linkedAProject := decodeFoundationSuccess[foundationProject](
			t,
			runReleaseCLI(t, executable, nested[linkedA], nil, "project", "show"),
			"project show",
		)
		linkedBProject := decodeFoundationSuccess[foundationProject](
			t,
			runReleaseCLI(t, executable, nested[linkedB], nil, "project", "show"),
			"project show",
		)
		otherProject := decodeFoundationSuccess[foundationProject](
			t,
			runReleaseCLI(t, executable, nested[otherRoot], nil, "project", "show"),
			"project show",
		)
		if len(mainProject.Workspaces) != 1 || len(linkedAProject.Workspaces) != 2 || len(linkedBProject.Workspaces) != 3 || len(otherProject.Workspaces) != 1 {
			t.Fatalf("registered release projects = main %#v; linked A %#v; linked B %#v; other %#v", mainProject, linkedAProject, linkedBProject, otherProject)
		}

		projects := decodeFoundationSuccess[[]foundationProject](
			t,
			runReleaseCLI(t, executable, common, nil, "project", "list"),
			"project list",
		)
		codes := make([]string, len(projects))
		for index, project := range projects {
			codes[index] = project.Code
		}
		sort.Strings(codes)
		if !reflect.DeepEqual(codes, []string{"other", "shared"}) {
			t.Fatalf("common-parent projects = %#v", projects)
		}

		description := "description supplied on stdin 世界\n"
		first := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(
				t,
				executable,
				nested[mainRoot],
				strings.NewReader(description),
				"add", "compiled-release-token one", "--description-file", "-", "--external-id", "Release:Exact", "--group", "Release/A",
			),
			"add",
		)
		second := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[linkedA], nil, "add", "compiled-release-token two", "--external-id", "Release:Exact", "--group", "Release/A"),
			"add",
		)
		third := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[linkedB], nil, "add", "compiled-release-token three", "--external-id", "Release:Exact", "--group", "Release/A"),
			"add",
		)
		other := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[otherRoot], nil, "add", "unrelated project survivor"),
			"add",
		)
		if first.ID != "shared-1" || first.Description != description || second.ID != "shared-2" || third.ID != "shared-3" || other.ID != "other-1" {
			t.Fatalf("project-local additions = %#v %#v %#v %#v", first, second, third, other)
		}

		claims := runCoreQueueCLIConcurrently(t, executable, []coreQueueInvocation{
			{directory: nested[linkedA], args: []string{"start-next", "--external-id", "Release:Exact", "--group", "Release/A"}},
			{directory: nested[linkedB], args: []string{"start-next", "--external-id", "Release:Exact", "--group", "Release/A"}},
		})
		linkedAClaim := decodeFoundationSuccess[foundationNext](t, claims[0], "start-next")
		linkedBClaim := decodeFoundationSuccess[foundationNext](t, claims[1], "start-next")
		if linkedAClaim.SelectionReason != "next_open" || linkedAClaim.Pellet == nil || linkedAClaim.Pellet.Workspace == nil ||
			linkedBClaim.SelectionReason != "next_open" || linkedBClaim.Pellet == nil || linkedBClaim.Pellet.Workspace == nil ||
			linkedAClaim.Pellet.ID == linkedBClaim.Pellet.ID || linkedAClaim.Pellet.Workspace.ID == linkedBClaim.Pellet.Workspace.ID {
			t.Fatalf("atomic linked-worktree claims = %#v and %#v", linkedAClaim, linkedBClaim)
		}

		conflict := runReleaseCLI(t, executable, nested[mainRoot], nil, "close", linkedAClaim.Pellet.ID)
		decodeCoreQueueError(t, conflict, 4, "pellet_in_progress_elsewhere")
		if output, err := foundationGitCommand(mainRoot, "worktree", "remove", "--force", linkedA); err != nil {
			t.Fatalf("remove owning release worktree: %v\n%s", err, output)
		}
		if _, err := os.Stat(linkedA); !os.IsNotExist(err) {
			t.Fatalf("removed worktree still exists at %q: %v", linkedA, err)
		}
		recovered := decodeFoundationSuccess[coreQueueLifecycle](
			t,
			runReleaseCLI(
				t,
				executable,
				nested[mainRoot],
				nil,
				"close", linkedAClaim.Pellet.ID,
				"--recover-workspace", strconv.FormatInt(linkedAClaim.Pellet.Workspace.ID, 10), "--yes",
			),
			"close",
		)
		if recovered.Status != "closed" || recovered.RecoveredWorkspace == nil || recovered.RecoveredWorkspace.ID != linkedAClaim.Pellet.Workspace.ID {
			t.Fatalf("removed-worktree recovery = %#v", recovered)
		}
		closedByOwner := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[linkedB], nil, "close", linkedBClaim.Pellet.ID),
			"close",
		)
		if closedByOwner.Status != "closed" || closedByOwner.Workspace != nil {
			t.Fatalf("linked owner close = %#v", closedByOwner)
		}

		mainClaim := decodeFoundationSuccess[foundationNext](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "start-next", "--external-id", "Release:Exact", "--group", "Release/A"),
			"start-next",
		)
		if mainClaim.Pellet == nil || mainClaim.Pellet.ID == linkedAClaim.Pellet.ID || mainClaim.Pellet.ID == linkedBClaim.Pellet.ID {
			t.Fatalf("main worktree did not receive the distinct remaining pellet: %#v", mainClaim)
		}
		decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "close", mainClaim.Pellet.ID),
			"close",
		)

		searched := decodeFoundationSuccess[[]foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "search", "compiled-release-token"),
			"search",
		)
		if len(searched) != 3 {
			t.Fatalf("closed release search = %#v", searched)
		}
		memoryText := "shared-1 remains project memory after purge 世界\n"
		memory := decodeFoundationSuccess[foundationMemory](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], strings.NewReader(memoryText), "memory", "add", "--file", "-"),
			"memory add",
		)
		if memory.Project != "shared" || memory.Text != memoryText {
			t.Fatalf("stdin memory = %#v", memory)
		}

		openSurvivor := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "add", "open survivor"),
			"add",
		)
		deferredSurvivor := decodeFoundationSuccess[foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "add", "deferred survivor", "--maybe-later"),
			"add",
		)
		if openSurvivor.ID != "shared-4" || openSurvivor.Status != "open" || deferredSurvivor.ID != "shared-5" || deferredSurvivor.Status != "maybe_later" {
			t.Fatalf("purge survivor setup = %#v %#v", openSurvivor, deferredSurvivor)
		}

		human := runReleaseCLI(t, executable, nested[mainRoot], nil, "--human", "list", "--all")
		if human.exit != 0 || human.stderr != "" || human.stdout == "" || strings.Contains(human.stdout, "\x1b[") {
			t.Fatalf("NO_COLOR human output = %#v", human)
		}

		preview := decodeFoundationSuccess[foundationPurge](
			t,
			runReleaseCLI(t, executable, common, nil, "purge", "--project", "shared", "--dry-run"),
			"purge",
		)
		if !preview.DryRun || preview.Count != 3 || len(preview.References) != 3 {
			t.Fatalf("release purge preview = %#v", preview)
		}
		purged := decodeFoundationSuccess[foundationPurge](
			t,
			runReleaseCLI(t, executable, common, nil, "purge", "--project", "shared", "--yes"),
			"purge",
		)
		if purged.DryRun || purged.Count != 3 || !reflect.DeepEqual(purged.References, preview.References) {
			t.Fatalf("release purge = %#v, preview %#v", purged, preview)
		}
		postPurgeSearch := decodeFoundationSuccess[[]foundationPellet](
			t,
			runReleaseCLI(t, executable, nested[mainRoot], nil, "search", "compiled-release-token"),
			"search",
		)
		if postPurgeSearch == nil || len(postPurgeSearch) != 0 {
			t.Fatalf("post-purge typed empty search = %#v", postPurgeSearch)
		}

		database, err := sqlite.Open(t.Context(), initialized.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM projects", 2)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM project_workspaces WHERE project_id = (SELECT project_id FROM projects WHERE code = 'shared')", 3)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM project_workspaces WHERE project_id = (SELECT project_id FROM projects WHERE code = 'other')", 1)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM project_workspaces WHERE workspace_id = ?", 1, linkedAClaim.Pellet.Workspace.ID)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM pellets", 3)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM pellets WHERE project_id = (SELECT project_id FROM projects WHERE code = 'shared') AND number = 4 AND status = 'open' AND priority IS NOT NULL", 1)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM pellets WHERE project_id = (SELECT project_id FROM projects WHERE code = 'shared') AND number = 5 AND status = 'maybe_later' AND priority IS NULL", 1)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM pellets WHERE project_id = (SELECT project_id FROM projects WHERE code = 'other') AND number = 1 AND status = 'open'", 1)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM pellets WHERE status IN ('closed', 'in_progress') OR workspace_id IS NOT NULL", 0)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM pellets_fts", 3)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM memories", 1)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM memories_fts", 1)
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM memories WHERE text = ?", 1, memoryText)
		assertFoundationQueryInt(t, database, "SELECT next_pellet_number FROM projects WHERE code = 'shared'", 6)
		assertFoundationQueryInt(t, database, "SELECT next_pellet_number FROM projects WHERE code = 'other'", 2)
	})
}

func runReleaseCLI(t *testing.T, executable, directory string, stdin io.Reader, args ...string) foundationResult {
	t.Helper()
	command, stdout, stderr := foundationCLICommand(executable, directory, args...)
	command.Env = releaseOfflineEnvironment(command.Env)
	if stdin != nil {
		command.Stdin = stdin
	}
	err := command.Run()
	return foundationProcessResult(t, stdout, stderr, err)
}

func releaseOfflineEnvironment(base []string) []string {
	blocked := map[string]bool{
		"NO_COLOR": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true,
		"http_proxy": true, "https_proxy": true, "all_proxy": true, "NO_PROXY": true, "no_proxy": true,
	}
	environment := make([]string, 0, len(base)+8)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !blocked[key] {
			environment = append(environment, entry)
		}
	}
	return append(
		environment,
		"NO_COLOR=1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=",
	)
}
