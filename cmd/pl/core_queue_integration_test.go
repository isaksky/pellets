package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"pellets/internal/storage/sqlite"
)

func TestCoreQueueCompiledProcessIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("core queue integration tests require native Git: %v", err)
	}
	executable := buildFoundationExecutable(t)
	failureExecutable := buildFoundationFailureExecutable(t)

	t.Run("concurrent additions preserve project-local allocation and exact filters", func(t *testing.T) {
		fixture := newCoreQueueCompiledFixture(t, executable, 3)
		otherRoot := filepath.Join(fixture.common, "independent project")
		createFoundationRepository(t, otherRoot)
		decodeFoundationSuccess[foundationProject](
			t, runFoundationCLI(t, executable, otherRoot, "init", "--code", "other"), "init",
		)

		calls := make([]coreQueueInvocation, 0, len(fixture.roots)+1)
		for index, root := range fixture.roots {
			calls = append(calls, coreQueueInvocation{
				directory: root,
				args: []string{
					"add", fmt.Sprintf("concurrent queue add %d", index),
					"--external-id", "Case:Exact", "--group", "Rollout/A",
				},
			})
		}
		calls = append(calls, coreQueueInvocation{
			directory: otherRoot,
			args:      []string{"add", "independent concurrent add", "--external-id", "Case:Exact", "--group", "Rollout/A"},
		})
		results := runCoreQueueCLIConcurrently(t, executable, calls)

		queuePellets := make([]foundationPellet, 0, len(fixture.roots))
		for index, result := range results {
			pellet := decodeFoundationSuccess[foundationPellet](t, result, "add")
			if index < len(fixture.roots) {
				queuePellets = append(queuePellets, pellet)
				if pellet.Project != "queue" {
					t.Fatalf("concurrent queue add returned project %q", pellet.Project)
				}
			} else if pellet.ID != "other-1" || pellet.Priority == nil || *pellet.Priority != 1024 {
				t.Fatalf("independent project allocation = %#v", pellet)
			}
		}
		sort.Slice(queuePellets, func(left, right int) bool { return queuePellets[left].Number < queuePellets[right].Number })
		for index, pellet := range queuePellets {
			wantNumber := int64(index + 1)
			if pellet.ID != fmt.Sprintf("queue-%d", wantNumber) || pellet.Priority == nil || *pellet.Priority != wantNumber*1024 {
				t.Fatalf("sorted concurrent queue add %d = %#v", index, pellet)
			}
		}

		decodeFoundationSuccess[foundationPellet](
			t,
			runFoundationCLI(
				t, executable, fixture.roots[0], "add", "wrong group",
				"--external-id", "Case:Exact", "--group", "rollout/a",
			),
			"add",
		)
		decodeFoundationSuccess[foundationPellet](
			t,
			runFoundationCLI(
				t, executable, fixture.roots[1], "add", "wrong external",
				"--external-id", "case:exact", "--group", "Rollout/A",
			),
			"add",
		)
		filtered := decodeFoundationSuccess[[]foundationPellet](
			t,
			runFoundationCLI(
				t, executable, fixture.roots[2], "--project", "queue", "list",
				"--external-id", "Case:Exact", "--group", "Rollout/A",
			),
			"list",
		)
		if len(filtered) != len(fixture.roots) {
			t.Fatalf("combined project/external-id/group filter = %#v", filtered)
		}
		for index, pellet := range filtered {
			if pellet.ID != fmt.Sprintf("queue-%d", index+1) || pellet.Project != "queue" {
				t.Fatalf("filtered pellet %d = %#v", index, pellet)
			}
		}
		next := decodeFoundationSuccess[foundationNext](
			t,
			runFoundationCLI(
				t, executable, fixture.roots[2], "next",
				"--external-id", "Case:Exact", "--group", "Rollout/A",
			),
			"next",
		)
		if next.SelectionReason != "next_open" || next.Pellet == nil || next.Pellet.ID != "queue-1" {
			t.Fatalf("combined filtered next = %#v", next)
		}

		beforeRollback := captureFoundationDatabaseState(t, fixture.databasePath)
		failedAdd := runFoundationCLIWithTemporaryTrigger(
			t,
			failureExecutable,
			fixture.roots[0],
			`CREATE TEMP TRIGGER core_queue_reject_add
			BEFORE INSERT ON pellets
			WHEN NEW.title = 'compiled rollback'
			BEGIN
				SELECT RAISE(ABORT, 'forced compiled add rollback');
			END`,
			"add",
			"compiled rollback",
		)
		decodeCoreQueueError(t, failedAdd, 5, "pellet_storage_failed")
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeRollback, "failed compiled add")
	})

	t.Run("start races return stable write-free workspace conflicts", func(t *testing.T) {
		fixture := newCoreQueueCompiledFixture(t, executable, 3)
		pellets := make([]foundationPellet, 3)
		for index := range pellets {
			pellets[index] = decodeFoundationSuccess[foundationPellet](
				t,
				runFoundationCLI(t, executable, fixture.roots[0], "add", fmt.Sprintf("start race %d", index)),
				"add",
			)
		}

		sameWorkspace := runCoreQueueCLIConcurrently(t, executable, []coreQueueInvocation{
			{directory: fixture.roots[0], args: []string{"start", pellets[0].ID}},
			{directory: fixture.roots[0], args: []string{"start", pellets[1].ID}},
		})
		raceWorkspaceConflict := failedCoreQueueResult(t, sameWorkspace)
		winnerID, loserID := assertStartRaceResults(t, sameWorkspace, "workspace_already_in_progress")
		assertCoreQueueQueryInt(t, fixture.databasePath, `
			SELECT COUNT(*) FROM pellets
			WHERE status = 'in_progress' AND workspace_id = ?`, 1, fixture.workspaceIDs[fixture.roots[0]])

		beforeStableWorkspaceConflict := captureFoundationDatabaseState(t, fixture.databasePath)
		stableWorkspaceConflict := runFoundationCLI(t, executable, fixture.roots[0], "start", loserID)
		workspaceError := decodeCoreQueueError(t, stableWorkspaceConflict, 4, "workspace_already_in_progress")
		if workspaceError.Error.Details["pellet_id"] != winnerID ||
			workspaceError.Error.Details["workspace_id"] != float64(fixture.workspaceIDs[fixture.roots[0]]) {
			t.Fatalf("stable workspace conflict details = %#v", workspaceError.Error.Details)
		}
		if stableWorkspaceConflict != raceWorkspaceConflict {
			t.Fatalf(
				"workspace conflict changed across retry:\nrace=%#v\nretry=%#v",
				raceWorkspaceConflict, stableWorkspaceConflict,
			)
		}
		assertCoreQueueDatabaseState(
			t, fixture.databasePath, beforeStableWorkspaceConflict, "stable workspace_already_in_progress retry",
		)
		decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, fixture.roots[0], "release", winnerID), "release",
		)

		samePellet := runCoreQueueCLIConcurrently(t, executable, []coreQueueInvocation{
			{directory: fixture.roots[1], args: []string{"start", pellets[2].ID}},
			{directory: fixture.roots[2], args: []string{"start", pellets[2].ID}},
		})
		racePelletConflict := failedCoreQueueResult(t, samePellet)
		_, _ = assertStartRaceResults(t, samePellet, "pellet_in_progress_elsewhere")
		losingRoot := fixture.roots[1]
		if samePellet[0].exit == 0 {
			losingRoot = fixture.roots[2]
		}
		beforeStablePelletConflict := captureFoundationDatabaseState(t, fixture.databasePath)
		stablePelletConflict := runFoundationCLI(t, executable, losingRoot, "start", pellets[2].ID)
		pelletError := decodeCoreQueueError(t, stablePelletConflict, 4, "pellet_in_progress_elsewhere")
		if pelletError.Error.Details["pellet_id"] != pellets[2].ID {
			t.Fatalf("stable pellet conflict details = %#v", pelletError.Error.Details)
		}
		if stablePelletConflict != racePelletConflict {
			t.Fatalf(
				"pellet conflict changed across retry:\nrace=%#v\nretry=%#v",
				racePelletConflict, stablePelletConflict,
			)
		}
		assertCoreQueueDatabaseState(
			t, fixture.databasePath, beforeStablePelletConflict, "stable pellet_in_progress_elsewhere retry",
		)
		assertCoreQueueQueryInt(t, fixture.databasePath, `
			SELECT COUNT(*) FROM (
				SELECT workspace_id
				FROM pellets
				WHERE status = 'in_progress'
				GROUP BY workspace_id
				HAVING COUNT(*) > 1
			)`, 0)
	})

	t.Run("start-next races select distinct work and bound empty retry and busy paths", func(t *testing.T) {
		fixture := newCoreQueueCompiledFixture(t, executable, 5)
		const eligibleCount = 3
		for index := 0; index < eligibleCount; index++ {
			decodeFoundationSuccess[foundationPellet](
				t,
				runFoundationCLI(
					t, executable, fixture.roots[0], "add", fmt.Sprintf("eligible %d", index),
					"--external-id", "Focus:Exact", "--group", "Workers/A",
				),
				"add",
			)
		}
		retryPellet := decodeFoundationSuccess[foundationPellet](
			t,
			runFoundationCLI(
				t, executable, fixture.roots[0], "add", "retry candidate",
				"--external-id", "Retry:Exact", "--group", "Workers/A",
			),
			"add",
		)

		calls := make([]coreQueueInvocation, len(fixture.roots))
		for index, root := range fixture.roots {
			calls[index] = coreQueueInvocation{
				directory: root,
				args: []string{
					"start-next", "--external-id", "Focus:Exact", "--group", "Workers/A",
				},
			}
		}
		results := runCoreQueueCLIConcurrently(t, executable, calls)
		claimed := make(map[string]int64, eligibleCount)
		emptyRoot := ""
		successRoot := ""
		for index, result := range results {
			selection := decodeFoundationSuccess[foundationNext](t, result, "start-next")
			switch selection.SelectionReason {
			case "next_open":
				if selection.Pellet == nil || selection.Pellet.Workspace == nil {
					t.Fatalf("compiled start-next claim %d = %#v", index, selection)
				}
				if _, duplicate := claimed[selection.Pellet.ID]; duplicate {
					t.Fatalf("compiled start-next returned duplicate pellet %s", selection.Pellet.ID)
				}
				if selection.Pellet.Workspace.ID != fixture.workspaceIDs[fixture.roots[index]] {
					t.Fatalf("compiled start-next owner = %#v, root %q", selection.Pellet.Workspace, fixture.roots[index])
				}
				claimed[selection.Pellet.ID] = selection.Pellet.Workspace.ID
				successRoot = fixture.roots[index]
			case "none":
				if selection.Pellet != nil {
					t.Fatalf("typed empty start-next %d = %#v", index, selection)
				}
				emptyRoot = fixture.roots[index]
			default:
				t.Fatalf("compiled start-next selection %d = %#v", index, selection)
			}
		}
		if len(claimed) != eligibleCount || emptyRoot == "" || successRoot == "" {
			t.Fatalf("compiled start-next claims = %#v, empty root = %q", claimed, emptyRoot)
		}
		for number := 1; number <= eligibleCount; number++ {
			if _, found := claimed[fmt.Sprintf("queue-%d", number)]; !found {
				t.Fatalf("compiled start-next did not claim eligible queue-%d: %#v", number, claimed)
			}
		}

		beforeEmpty := captureFoundationDatabaseState(t, fixture.databasePath)
		empty := decodeFoundationSuccess[foundationNext](
			t,
			runFoundationCLI(
				t, executable, emptyRoot, "start-next",
				"--external-id", "Focus:Exact", "--group", "Workers/A",
			),
			"start-next",
		)
		if empty.SelectionReason != "none" || empty.Pellet != nil {
			t.Fatalf("stable typed empty start-next = %#v", empty)
		}
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeEmpty, "typed empty start-next")

		beforeResume := captureFoundationDatabaseState(t, fixture.databasePath)
		resumed := decodeFoundationSuccess[foundationNext](
			t,
			runFoundationCLI(
				t, executable, successRoot, "start-next",
				"--external-id", "does-not-match", "--group", "does-not-match",
			),
			"start-next",
		)
		if resumed.SelectionReason != "resume_in_progress" || resumed.Pellet == nil || resumed.Pellet.Workspace == nil || resumed.Pellet.Workspace.ID != fixture.workspaceIDs[successRoot] {
			t.Fatalf("filtered start-next resume = %#v", resumed)
		}
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeResume, "read-only start-next resume")

		beforeRetry := captureFoundationDatabaseState(t, fixture.databasePath)
		retryStarted := time.Now()
		firstRetry := runFoundationCLIWithTemporaryTrigger(
			t,
			failureExecutable,
			emptyRoot,
			`CREATE TEMP TRIGGER core_queue_reject_start_next
			BEFORE UPDATE ON pellets
			WHEN OLD.status = 'open' AND NEW.status = 'in_progress'
			BEGIN
				SELECT RAISE(ABORT, 'forced compiled start-next retry');
			END`,
			"start-next", "--external-id", "Retry:Exact", "--group", "Workers/A",
		)
		firstElapsed := time.Since(retryStarted)
		retryError := decodeCoreQueueError(t, firstRetry, 4, "start_next_conflict")
		if retryError.Error.Details["attempts"] != float64(2) || firstElapsed > 2*time.Second {
			t.Fatalf("bounded start-next retry = elapsed %s details %#v", firstElapsed, retryError.Error.Details)
		}
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeRetry, "first bounded start-next retry")
		secondRetry := runFoundationCLIWithTemporaryTrigger(
			t,
			failureExecutable,
			emptyRoot,
			`CREATE TEMP TRIGGER core_queue_reject_start_next
			BEFORE UPDATE ON pellets
			WHEN OLD.status = 'open' AND NEW.status = 'in_progress'
			BEGIN
				SELECT RAISE(ABORT, 'forced compiled start-next retry');
			END`,
			"start-next", "--external-id", "Retry:Exact", "--group", "Workers/A",
		)
		decodeCoreQueueError(t, secondRetry, 4, "start_next_conflict")
		if secondRetry != firstRetry {
			t.Fatalf("deterministic retry changed result:\nfirst=%#v\nsecond=%#v", firstRetry, secondRetry)
		}
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeRetry, "second bounded start-next retry")

		database, err := sqlite.Open(context.Background(), fixture.databasePath)
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
		if _, err := connection.ExecContext(
			context.Background(),
			"UPDATE pellets SET title = 'uncommitted title' WHERE number = ? AND project_id = (SELECT project_id FROM projects WHERE code = 'queue')",
			retryPellet.Number,
		); err != nil {
			connection.ExecContext(context.Background(), "ROLLBACK")
			connection.Close()
			database.Close()
			t.Fatal(err)
		}
		readDuringWrite := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, emptyRoot, "show", retryPellet.ID), "show",
		)
		if readDuringWrite.Title != retryPellet.Title {
			t.Fatalf("read during write observed %q, want committed %q", readDuringWrite.Title, retryPellet.Title)
		}
		if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}

		beforeBusy := captureFoundationDatabaseState(t, fixture.databasePath)
		database, err = sqlite.Open(context.Background(), fixture.databasePath)
		if err != nil {
			t.Fatal(err)
		}
		connection, err = database.Conn(context.Background())
		if err != nil {
			database.Close()
			t.Fatal(err)
		}
		if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			connection.Close()
			database.Close()
			t.Fatal(err)
		}
		busyStarted := time.Now()
		busyResult := runFoundationCLI(
			t, executable, emptyRoot, "start-next",
			"--external-id", "Retry:Exact", "--group", "Workers/A",
		)
		busyElapsed := time.Since(busyStarted)
		decodeCoreQueueError(t, busyResult, 4, "database_busy")
		if busyElapsed < 4*time.Second || busyElapsed > 10*time.Second {
			t.Fatalf("compiled busy wait elapsed %s, want bounded wait near configured five seconds", busyElapsed)
		}
		if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeBusy, "database_busy start-next")
	})

	t.Run("removed worktree requires explicit confirmed release recovery", func(t *testing.T) {
		fixture := newCoreQueueCompiledFixture(t, executable, 2)
		pellet := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, fixture.roots[0], "add", "stale workspace owner"), "add",
		)
		started := decodeFoundationSuccess[foundationPellet](
			t, runFoundationCLI(t, executable, fixture.roots[1], "start", pellet.ID), "start",
		)
		staleWorkspaceID := fixture.workspaceIDs[fixture.roots[1]]
		if started.Workspace == nil || started.Workspace.ID != staleWorkspaceID {
			t.Fatalf("stale recovery owner = %#v", started.Workspace)
		}
		runFoundationGit(t, fixture.roots[0], "worktree", "remove", fixture.roots[1])
		assertFoundationPathAbsent(t, fixture.roots[1])

		beforeUnconfirmed := captureFoundationDatabaseState(t, fixture.databasePath)
		withoutRecovery := runFoundationCLI(t, executable, fixture.roots[0], "release", pellet.ID)
		decodeCoreQueueError(t, withoutRecovery, 4, "pellet_in_progress_elsewhere")
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeUnconfirmed, "cross-workspace release")

		workspaceID := strconv.FormatInt(staleWorkspaceID, 10)
		withoutYes := runFoundationCLI(
			t, executable, fixture.roots[0], "release", pellet.ID, "--recover-workspace", workspaceID,
		)
		decodeCoreQueueError(t, withoutYes, 6, "confirmation_required")
		assertCoreQueueDatabaseState(t, fixture.databasePath, beforeUnconfirmed, "unconfirmed stale-worktree recovery")

		recovered := decodeFoundationSuccess[coreQueueLifecycle](
			t,
			runFoundationCLI(
				t, executable, fixture.roots[0], "release", pellet.ID,
				"--recover-workspace", workspaceID, "--yes",
			),
			"release",
		)
		if recovered.ID != pellet.ID || recovered.Status != "open" || recovered.Priority == nil || recovered.Workspace != nil || recovered.RecoveredWorkspace == nil || recovered.RecoveredWorkspace.ID != staleWorkspaceID {
			t.Fatalf("explicit stale-worktree recovery = %#v", recovered)
		}
	})

	t.Run("ownerless close and defer reject recovery without writes", func(t *testing.T) {
		fixture := newCoreQueueCompiledFixture(t, executable, 2)
		registeredWorkspaceID := fixture.workspaceIDs[fixture.roots[1]]
		nonexistentWorkspaceID := registeredWorkspaceID + 1000
		for _, operation := range []string{"close", "defer"} {
			pellet := decodeFoundationSuccess[foundationPellet](
				t, runFoundationCLI(t, executable, fixture.roots[0], "add", "ownerless "+operation), "add",
			)
			for _, workspaceID := range []int64{registeredWorkspaceID, nonexistentWorkspaceID} {
				before := captureFoundationDatabaseState(t, fixture.databasePath)
				result := runFoundationCLI(
					t, executable, fixture.roots[0], operation, pellet.ID,
					"--recover-workspace", strconv.FormatInt(workspaceID, 10), "--yes",
				)
				envelope := decodeCoreQueueError(t, result, 4, "recovery_workspace_mismatch")
				if envelope.Error.Details["pellet_id"] != pellet.ID || envelope.Error.Details["owner_workspace_id"] != nil || envelope.Error.Details["provided_workspace_id"] != float64(workspaceID) {
					t.Fatalf("compiled ownerless %s recovery details = %#v", operation, envelope.Error.Details)
				}
				assertCoreQueueDatabaseState(t, fixture.databasePath, before, "rejected ownerless "+operation+" recovery")
			}

			transitioned := decodeFoundationSuccess[foundationPellet](
				t, runFoundationCLI(t, executable, fixture.roots[0], operation, pellet.ID), operation,
			)
			wantStatus := "closed"
			if operation == "defer" {
				wantStatus = "maybe_later"
			}
			if transitioned.Status != wantStatus || transitioned.Workspace != nil {
				t.Fatalf("normal compiled ownerless %s = %#v", operation, transitioned)
			}

			repeated := decodeFoundationSuccess[foundationPellet](
				t,
				runFoundationCLI(
					t, executable, fixture.roots[1], operation, pellet.ID,
					"--recover-workspace", strconv.FormatInt(registeredWorkspaceID, 10), "--yes",
				),
				operation,
			)
			if !reflect.DeepEqual(repeated, transitioned) {
				t.Fatalf("compiled idempotent %s with recovery tuple changed result: first=%#v repeat=%#v", operation, transitioned, repeated)
			}
		}
	})
}

type coreQueueCompiledFixture struct {
	common       string
	databasePath string
	roots        []string
	workspaceIDs map[string]int64
}

func newCoreQueueCompiledFixture(t *testing.T, executable string, workspaceCount int) coreQueueCompiledFixture {
	t.Helper()
	if workspaceCount < 1 {
		t.Fatal("core queue fixture needs at least one workspace")
	}
	common := filepath.Join(foundationShortTempDir(t), "core queue compiled")
	mainRoot := filepath.Join(common, "main")
	createFoundationRepository(t, mainRoot)
	roots := []string{mainRoot}
	for index := 1; index < workspaceCount; index++ {
		root := filepath.Join(common, fmt.Sprintf("worker-%d", index))
		if output, err := foundationGitCommand(
			mainRoot, "worktree", "add", "--quiet", "-b", fmt.Sprintf("core-queue-worker-%d", index), root,
		); err != nil {
			t.Fatalf("add core queue linked worktree %d: %v\n%s", index, err, output)
		}
		roots = append(roots, root)
	}

	initialized := decodeFoundationSuccess[foundationInitDB](
		t, runFoundationCLI(t, executable, common, "init-db"), "init-db",
	)
	var project foundationProject
	for _, root := range roots {
		project = decodeFoundationSuccess[foundationProject](
			t, runFoundationCLI(t, executable, root, "init", "--code", "queue"), "init",
		)
	}
	workspaceIDs := make(map[string]int64, len(roots))
	for _, root := range roots {
		wantPath := filepath.ToSlash(filepath.Base(root))
		for _, workspace := range project.Workspaces {
			if workspace.RootPath == wantPath {
				workspaceIDs[root] = workspace.ID
				break
			}
		}
		if workspaceIDs[root] == 0 {
			t.Fatalf("registered project %#v has no workspace for %q", project, root)
		}
	}
	return coreQueueCompiledFixture{
		common: common, databasePath: initialized.DatabasePath, roots: roots,
		workspaceIDs: workspaceIDs,
	}
}

type coreQueueInvocation struct {
	directory string
	args      []string
}

type coreQueueConcurrentResult struct {
	result     foundationResult
	unexpected error
}

func runCoreQueueCLIConcurrently(
	t *testing.T,
	executable string,
	calls []coreQueueInvocation,
) []foundationResult {
	t.Helper()
	start := make(chan struct{})
	concurrent := make([]coreQueueConcurrentResult, len(calls))
	var group sync.WaitGroup
	for index, call := range calls {
		group.Add(1)
		go func(index int, call coreQueueInvocation) {
			defer group.Done()
			command, stdout, stderr := foundationCLICommand(executable, call.directory, call.args...)
			<-start
			err := command.Run()
			result := foundationResult{stdout: stdout.String(), stderr: stderr.String()}
			if err != nil {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					concurrent[index].unexpected = err
					return
				}
				result.exit = exitError.ExitCode()
			}
			concurrent[index].result = result
		}(index, call)
	}
	close(start)
	group.Wait()

	results := make([]foundationResult, len(calls))
	for index, result := range concurrent {
		if result.unexpected != nil {
			t.Fatalf("run concurrent compiled CLI %v in %q: %v", calls[index].args, calls[index].directory, result.unexpected)
		}
		results[index] = result.result
	}
	return results
}

func assertStartRaceResults(t *testing.T, results []foundationResult, conflictCode string) (string, string) {
	t.Helper()
	if len(results) != 2 {
		t.Fatalf("start race returned %d results", len(results))
	}
	winnerID, loserID := "", ""
	for _, result := range results {
		if result.exit == 0 {
			if winnerID != "" {
				t.Fatalf("start race returned two successes: %#v", results)
			}
			winnerID = decodeFoundationSuccess[foundationPellet](t, result, "start").ID
			continue
		}
		if loserID != "" {
			t.Fatalf("start race returned two failures: %#v", results)
		}
		errorEnvelope := decodeCoreQueueError(t, result, 4, conflictCode)
		if conflictCode == "workspace_already_in_progress" {
			loserID, _ = errorEnvelope.Error.Details["pellet_id"].(string)
		} else {
			loserID, _ = errorEnvelope.Error.Details["pellet_id"].(string)
		}
	}
	if winnerID == "" || loserID == "" {
		t.Fatalf("start race results = %#v", results)
	}
	if conflictCode == "workspace_already_in_progress" {
		// The error names the pellet already owned by the winner. The caller
		// needs the attempted loser, which is the other known successful input.
		if loserID != winnerID {
			t.Fatalf("workspace conflict named %q, want winning pellet %q", loserID, winnerID)
		}
		for _, candidate := range []string{"queue-1", "queue-2"} {
			if candidate != winnerID {
				loserID = candidate
				break
			}
		}
	}
	return winnerID, loserID
}

func failedCoreQueueResult(t *testing.T, results []foundationResult) foundationResult {
	t.Helper()
	var failed foundationResult
	failures := 0
	for _, result := range results {
		if result.exit != 0 {
			failed = result
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("concurrent race failures = %d, want 1: %#v", failures, results)
	}
	return failed
}

func decodeCoreQueueError(t *testing.T, result foundationResult, exit int, code string) foundationError {
	t.Helper()
	if result.exit != exit || result.stdout != "" {
		t.Fatalf("compiled error = exit %d stdout %q stderr %q, want exit %d and empty stdout", result.exit, result.stdout, result.stderr, exit)
	}
	var envelope foundationError
	if err := json.Unmarshal([]byte(result.stderr), &envelope); err != nil {
		t.Fatalf("decode compiled error %q: %v", result.stderr, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Error.Code != code {
		t.Fatalf("compiled error envelope = %#v, want code %q", envelope, code)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if want := string(encoded) + "\n"; result.stderr != want {
		t.Fatalf("compiled error stderr = %q, want compact envelope %q", result.stderr, want)
	}
	return envelope
}

func assertCoreQueueDatabaseState(
	t *testing.T,
	databasePath string,
	want foundationDatabaseState,
	operation string,
) {
	t.Helper()
	got := captureFoundationDatabaseState(t, databasePath)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s changed database state:\nbefore=%#v\nafter=%#v", operation, want, got)
	}
}

func assertCoreQueueQueryInt(t *testing.T, databasePath, query string, want int, args ...any) {
	t.Helper()
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var got int
	if err := database.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query result = %d, want %d; query: %s", got, want, query)
	}
}

type coreQueueLifecycle struct {
	ID                 string                     `json:"id"`
	Project            string                     `json:"project"`
	Number             int64                      `json:"number"`
	Title              string                     `json:"title"`
	Description        string                     `json:"description"`
	ExternalID         *string                    `json:"external_id"`
	Group              *string                    `json:"group"`
	Status             string                     `json:"status"`
	Priority           *int64                     `json:"priority"`
	Workspace          *foundationPelletWorkspace `json:"workspace"`
	CreatedAt          string                     `json:"created_at"`
	UpdatedAt          string                     `json:"updated_at"`
	CompletedAt        *string                    `json:"completed_at"`
	RecoveredWorkspace *foundationPelletWorkspace `json:"recovered_workspace"`
}
