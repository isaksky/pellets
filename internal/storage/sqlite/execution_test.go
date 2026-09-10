package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func runCapture(project, workspace, pellet int64) storage.RunCapture {
	return storage.RunCapture{ProjectID: project, WorkspaceID: workspace, PelletNumber: pellet, Mode: "run_one", StartingHead: strings.Repeat("a", 40),
		Settings:     storage.EffectiveRunSettings{Codex: storage.CodexRunSettings{Executable: "codex", Model: "future-model", ReasoningEffort: "high", Limits: storage.CodexRunLimits{MaxMessageBytes: 4096, EventBuffer: 8, MaxPending: 8, StderrBytes: 1024}}, ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"},
		PromptPrefix: storage.PromptPrefix{TemplateVersion: "test-prefix", SkillSHA256: strings.Repeat("a", 64), HelpSHA256: strings.Repeat("b", 64), ToolExecutable: "pl", ToolVersion: "test", Text: "stable\n"}}
}

func TestPromptPrefixEncodedStorageBoundAccountsForJSONEscapes(t *testing.T) {
	prefix := storage.PromptPrefix{TemplateVersion: "test-prefix", SkillSHA256: strings.Repeat("a", 64), HelpSHA256: strings.Repeat("b", 64), ToolExecutable: strings.Repeat("\x01", 4096), ToolVersion: strings.Repeat("\x01", 512), Text: strings.Repeat("\x01", 200000)}
	if err := storage.ValidatePromptPrefix(prefix); err != nil {
		t.Fatalf("escaped prefix unexpectedly rejected: %v", err)
	}
	prefix.Text = strings.Repeat("\x01", 400000)
	if err := storage.ValidatePromptPrefix(prefix); domain.PublicError(err).Code != "invalid_execution_run" {
		t.Fatalf("oversized encoded prefix error = %v", err)
	}
}

func TestPendingInteractionRoundTripsAndClearsWithoutAnswers(t *testing.T) {
	db, run, _ := createTestRun(t)
	interaction := &storage.RunInteraction{RequestID: `"request-1"`, Method: "item/tool/requestUserInput", ThreadID: "thread", TurnID: "turn", ItemID: "item", Title: "Codex needs your input", Questions: []storage.InteractionQuestion{{ID: "scope", Header: "Scope", Question: "Which scope?", Options: []storage.InteractionOption{{Label: "Focused", Description: "Only the target."}}, Other: true, Secret: true}}}
	progress := storage.RunProgress{Phase: "implementation", State: "awaiting_input", ThreadID: "thread", TurnID: "turn", Summary: "Codex is awaiting explicit input.", Interaction: interaction}
	run = updateRun(t, db, run, progress, "")
	read, err := db.ReadExecutionRun(context.Background(), run.ID)
	if err != nil || !reflect.DeepEqual(read.Interaction, interaction) {
		t.Fatalf("pending interaction round trip = %#v, %v", read.Interaction, err)
	}
	progress = read.RunProgress
	progress.State, progress.Interaction = "running", nil
	read = updateRun(t, db, read, progress, "")
	if read.Interaction != nil {
		t.Fatalf("cleared interaction retained: %#v", read.Interaction)
	}
	var stored string
	if err := db.db.QueryRowContext(context.Background(), `SELECT interaction_json FROM execution_runs WHERE run_id=?`, run.ID).Scan(&stored); err != nil || strings.Contains(stored, "answer") {
		t.Fatalf("unexpected answer storage: %q, %v", stored, err)
	}
}

func TestFailedPendingOperationClearsPendingInteractionAtomically(t *testing.T) {
	db, run, _ := createTestRun(t)
	interaction := &storage.RunInteraction{RequestID: `"request-1"`, Method: "item/tool/requestUserInput", ThreadID: "thread", TurnID: "turn", ItemID: "item", Title: "Codex needs your input", Questions: []storage.InteractionQuestion{{ID: "scope", Header: "Scope", Question: "Which scope?"}}}
	run = updateRun(t, db, run, storage.RunProgress{Phase: "implementation", State: "awaiting_input", ThreadID: "thread", TurnID: "turn", Interaction: interaction}, "")
	pending, err := db.BeginExecutionOperation(context.Background(), run.ID, run.Revision, "turn/interrupt")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := db.FinishExecutionOperation(context.Background(), storage.ExecutionOperationResult{ID: run.ID, PendingRevision: pending.PendingRevision, ErrorCode: "codex_call_failed"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != "needs_attention" || failed.Interaction != nil || failed.PendingOperation != "" {
		t.Fatalf("failed operation retained interaction or fence: %#v", failed)
	}
	reloaded, err := db.ReadExecutionRun(context.Background(), run.ID)
	if err != nil || reloaded.Interaction != nil || reloaded.PendingOperation != "" {
		t.Fatalf("durable failed operation state = %#v, %v", reloaded, err)
	}
}

func TestPreThreadRetryKeepsFreshPromptPrefix(t *testing.T) {
	db, run, _ := createTestRun(t)
	stopped := updateRun(t, db, run, storage.RunProgress{Phase: "preflight", State: "interrupted", Outcome: "unknown"}, "")
	fresh := stopped.RunCapture
	previous := stopped.ID
	fresh.ResumeFrom = &previous
	fresh.PromptPrefix = storage.PromptPrefix{TemplateVersion: "test-prefix", SkillSHA256: strings.Repeat("c", 64), HelpSHA256: strings.Repeat("d", 64), ToolExecutable: "changed-pl", ToolVersion: "changed", Text: "fresh prefix\n"}
	retried, err := db.CreateExecutionRun(context.Background(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ThreadID != "" || retried.PromptPrefix != fresh.PromptPrefix {
		t.Fatalf("pre-thread retry inherited stale conversation: %#v", retried)
	}
}

func TestFinalizationEvidenceIsImmutableAndInheritedExactly(t *testing.T) {
	db, run, _ := createTestRun(t)
	progress := storage.RunProgress{Phase: "close", State: "running", ThreadID: "thread", TurnID: "turn", Finalization: &storage.FinalizationEvidence{Files: []string{"source.go"}, Tree: strings.Repeat("c", 40), Subject: "demo-1: implement"}}
	run = updateRun(t, db, run, progress, strings.Repeat("b", 40))
	changed := progress
	changed.Finalization = &storage.FinalizationEvidence{Files: []string{"unrelated.go"}, Tree: progress.Finalization.Tree, Subject: progress.Finalization.Subject}
	if _, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: changed}); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("changed finalization accepted: %v", err)
	}
	progress.State, progress.Outcome = "interrupted", "unknown"
	stopped := updateRun(t, db, run, progress, "")
	capture := stopped.RunCapture
	capture.ResumeFrom = &stopped.ID
	capture.StartingHead = strings.Repeat("d", 40)
	resumed, err := db.CreateExecutionRun(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.StartingHead != stopped.StartingHead || resumed.ResultCommit != stopped.ResultCommit || resumed.TurnID != stopped.TurnID || resumed.Phase != "close" || !reflect.DeepEqual(resumed.Finalization, stopped.Finalization) || !reflect.DeepEqual(resumed.CommitVerifiedAt, stopped.CommitVerifiedAt) {
		t.Fatalf("resume replaced finalization evidence: %+v", resumed)
	}
}

func createTestRun(t *testing.T) (*ProjectDatabase, storage.ExecutionRun, pelletRepositoryFixture) {
	t.Helper()
	fixture := newPelletRepositoryFixture(t)
	repo := fixture.open(t)
	pellet, err := repo.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "Original title", Description: "Exact original description"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.TransitionPellet(context.Background(), fixture.main, pellet.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExecutionRunDatabase(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	run, err := db.CreateExecutionRun(context.Background(), runCapture(fixture.main.Project.ID, fixture.main.Workspace.ID, pellet.Reference.Number))
	if err != nil {
		t.Fatal(err)
	}
	return db, run, fixture
}

func updateRun(t *testing.T, db *ProjectDatabase, run storage.ExecutionRun, progress storage.RunProgress, commit string) storage.ExecutionRun {
	t.Helper()
	updated, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: progress, VerifiedCommit: commit})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func TestExecutionMigrationFromReleasedFixtureAndRollback(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "released.db")
	copyDatabaseFixture(t, path, "testdata/released-v1.db")
	raw := openRawDatabase(t, path)
	seedVersionOneWorkspaceFixture(t, raw)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenExecutionRunDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	assertPragmaInt(t, db.db, "user_version", LatestSchemaVersion)
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM application_metadata WHERE key='fixture' AND value='released-v1'`, 1)
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM execution_runs`, 0)
	var project, workspace, number int64
	if err := db.db.QueryRow(`SELECT project_id, workspace_id, number FROM pellets WHERE status='in_progress' LIMIT 1`).Scan(&project, &workspace, &number); err != nil {
		t.Fatal(err)
	}
	capture := runCapture(project, workspace, number)
	run, err := db.CreateExecutionRun(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	run = updateRun(t, db, run, storage.RunProgress{Phase: "commit", State: "interrupted", Outcome: "unknown", ThreadID: "released-thread", TurnID: "released-turn", ErrorCode: "supervisor_stopped"}, "")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenExecutionRunDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, err := db.ReadExecutionRun(context.Background(), run.ID)
	if err != nil || !reflect.DeepEqual(loaded, run) {
		t.Fatalf("reopen = %#v, %v; want %#v", loaded, err, run)
	}
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM pragma_foreign_key_check`, 0)

	rollbackPath := filepath.Join(t.TempDir(), "rollback.db")
	old, err := openWithMigrations(context.Background(), rollbackPath, migrations[:6])
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	sequence := append([]migration(nil), migrations...)
	sequence[6].assert = func(context.Context, *sql.Conn) error { return errors.New("injected") }
	failed, err := openWithMigrations(context.Background(), rollbackPath, sequence)
	if failed != nil || err == nil {
		t.Fatal("failed migration succeeded")
	}
	assertMigrationRolledBack(t, rollbackPath, "execution_runs", 6)
}

func TestExecutionInterruptedPhasesExplicitResumeAndRename(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"preflight", "thread_start", "turn_start", "implementation", "verification", "commit", "close", "review", "finalization"} {
		t.Run(phase, func(t *testing.T) {
			db, run, _ := createTestRun(t)
			run = updateRun(t, db, run, storage.RunProgress{Phase: phase, State: "interrupted", Outcome: "unknown", ThreadID: "thread-1", TurnID: "turn-1"}, "")
			plan, err := db.PlanProjectRename(context.Background(), run.ProjectID, "renamed")
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.RenameProject(context.Background(), storage.ProjectRenameRequest{ProjectID: plan.Project.ID, NewCode: plan.NewCode})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := db.ReadExecutionRun(context.Background(), run.ID)
			if err != nil || loaded.ProjectCode != "renamed" || loaded.ProjectID != run.ProjectID || loaded.Phase != phase {
				t.Fatalf("rename lost run: %#v %v", loaded, err)
			}
			capture := run.RunCapture
			capture.ResumeFrom = &run.ID
			resumed, err := db.CreateExecutionRun(context.Background(), capture)
			if err != nil || resumed.Attempt != 2 || resumed.ThreadID != run.ThreadID || resumed.TurnID != "" || resumed.ID == run.ID {
				t.Fatalf("resume: %#v %v", resumed, err)
			}
			original, err := db.ReadExecutionRun(context.Background(), run.ID)
			if err != nil || original.State != "interrupted" || original.TurnID != "turn-1" {
				t.Fatalf("resume changed original: %#v %v", original, err)
			}
			if _, err := db.CreateExecutionRun(context.Background(), capture); domain.PublicError(err).Code != "execution_run_conflict" {
				t.Fatalf("duplicate resume: %v", err)
			}
		})
	}
}

func TestExecutionSnapshotPurgeAndBoundedRetention(t *testing.T) {
	t.Parallel()
	db, run, fixture := createTestRun(t)
	for i := 0; i < storage.MaxRunActivity+10; i++ {
		run = updateRun(t, db, run, storage.RunProgress{Phase: "implementation", State: "running", ThreadID: "thread-1", TurnID: "turn-1", Summary: strings.Repeat("x", storage.MaxRunSummaryBytes)}, "")
	}
	if len(run.Activity) != storage.MaxRunActivity {
		t.Fatalf("activity count %d", len(run.Activity))
	}
	run = updateRun(t, db, run, storage.RunProgress{Phase: "finalization", State: "completed", Outcome: "succeeded", ThreadID: "thread-1", TurnID: "turn-1", Summary: "Verified result retained"}, strings.Repeat("b", 40))
	repo := fixture.open(t)
	defer repo.Close()
	reference := domain.PelletReference{ProjectCode: fixture.main.Project.Code, Number: run.PelletNumber}
	changedTitle := "Later title"
	if _, err := repo.UpdatePellet(context.Background(), fixture.main, reference, storage.PelletChanges{Title: &changedTitle}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TransitionPellet(context.Background(), fixture.main, reference, storage.PelletLifecycleRequest{Operation: storage.PelletClose}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PurgeClosedPellets(context.Background(), fixture.main.Project, storage.PelletPurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	count, err := db.PruneRunActivity(context.Background(), time.Now().Add(time.Hour), 1)
	if err != nil || count != 1 {
		t.Fatalf("retention %d %v", count, err)
	}
	retained, err := db.ReadExecutionRun(context.Background(), run.ID)
	if err != nil || retained.PelletPresent || retained.PelletTitle != "Original title" || retained.PelletDescription != "Exact original description" || retained.ResultCommit != run.ResultCommit || retained.CommitVerifiedAt == nil || len(retained.Activity) != 0 || !retained.ActivityPruned || retained.ThreadID != run.ThreadID {
		t.Fatalf("retained evidence %#v %v", retained, err)
	}
	if _, err := db.ReadExecutionRun(context.Background(), run.ID+999); domain.PublicError(err).Code != "execution_run_not_found" {
		t.Fatalf("missing exact target: %v", err)
	}
}

func TestExecutionConflictsBoundsAndQueueSeparation(t *testing.T) {
	t.Parallel()
	db, run, fixture := createTestRun(t)
	if _, err := db.CreateExecutionRun(context.Background(), run.RunCapture); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("active conflict: %v", err)
	}
	bad := run.RunCapture
	bad.WorkspaceID = fixture.other.Workspace.ID
	if _, err := db.CreateExecutionRun(context.Background(), bad); err == nil {
		t.Fatal("cross-project run accepted")
	}
	p := storage.RunProgress{Phase: "implementation", State: "awaiting_input", ThreadID: "t", TurnID: "u"}
	run = updateRun(t, db, run, p, "")
	if _, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: 1, Progress: p}); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("stale write %v", err)
	}
	p.Summary = strings.Repeat("界", storage.MaxRunSummaryBytes)
	if _, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: p}); err == nil {
		t.Fatal("unbounded summary accepted")
	}
	p.Summary = ""
	p.State = "completed"
	p.Outcome = "succeeded"
	if _, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: p}); err == nil {
		t.Fatal("missing commit treated as success")
	}
	if n, err := db.PruneRunActivity(context.Background(), time.Now().Add(time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("active run pruned %d %v", n, err)
	}
	assertQueryInt(t, db.db, `SELECT COUNT(*) FROM pellets WHERE status='in_progress'`, 1)
	listed, err := db.ListWorkspaceRuns(context.Background(), run.WorkspaceID, 0, 1)
	if err != nil || len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("bounded listing %v %v", listed, err)
	}
	listed, err = db.ListWorkspaceRuns(context.Background(), run.WorkspaceID, run.ID, 1)
	if err != nil || len(listed) != 0 {
		t.Fatalf("cursor listing %v %v", listed, err)
	}
}

func TestExecutionConcurrentUpdatesCannotOverwriteEvidence(t *testing.T) {
	t.Parallel()
	db, run, fixture := createTestRun(t)
	other, err := OpenExecutionRunDatabase(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, writer := range []*ProjectDatabase{db, other} {
		wg.Add(1)
		go func(writer *ProjectDatabase) {
			defer wg.Done()
			_, err := writer.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: run.ID, ExpectedRevision: run.Revision, Progress: storage.RunProgress{Phase: "commit", State: "interrupted", Outcome: "unknown"}})
			results <- err
		}(writer)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if domain.PublicError(err).Code == "execution_run_conflict" {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent writes success=%d conflict=%d", success, conflict)
	}
}

func TestExecutionExactCaptureAndCommitEvidenceCannotChange(t *testing.T) {
	t.Parallel()
	db, run, fixture := createTestRun(t)
	run = updateRun(t, db, run, storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "unknown"}, "")
	capture := run.RunCapture
	group, external := " exact group ", "source:Some/Issue#12"
	queue := fixture.open(t)
	defer queue.Close()
	if _, err := queue.UpdatePellet(context.Background(), fixture.main, domain.PelletReference{ProjectCode: fixture.main.Project.Code, Number: run.PelletNumber}, storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &group}, ExternalID: storage.NullableTextChange{Set: true, Value: &external}}); err != nil {
		t.Fatal(err)
	}
	capture.Group, capture.ExternalID, capture.ResumeFrom = &group, &external, &run.ID
	capture.Mode = "watch"
	capture.Settings.Codex.Model = "new-effective-model"
	resumed, err := db.CreateExecutionRun(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(resumed.RunCapture, capture) {
		t.Fatalf("capture changed: %#v", resumed.RunCapture)
	}
	progress := storage.RunProgress{Phase: "commit", State: "running", ThreadID: "thread", TurnID: "turn"}
	resumed = updateRun(t, db, resumed, progress, strings.Repeat("b", 40))
	if _, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: resumed.ID, ExpectedRevision: resumed.Revision, Progress: progress, VerifiedCommit: strings.Repeat("c", 40)}); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("commit evidence changed: %v", err)
	}
	if _, err := db.UpdateExecutionRun(context.Background(), storage.UpdateExecutionRun{ID: resumed.ID, ExpectedRevision: resumed.Revision, Progress: storage.RunProgress{Phase: "implementation", State: "running", ThreadID: "other-thread", TurnID: "turn"}}); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("thread identity changed: %v", err)
	}
	unchanged, err := db.ReadExecutionRun(context.Background(), resumed.ID)
	if err != nil || !reflect.DeepEqual(unchanged, resumed) {
		t.Fatalf("rejected writes changed evidence: %#v %v", unchanged, err)
	}
}

func TestExecutionPendingOperationSurvivesReconnectAndTerminalProgress(t *testing.T) {
	t.Parallel()
	db, run, fixture := createTestRun(t)
	run = updateRun(t, db, run, storage.RunProgress{Phase: "implementation", State: "running", ThreadID: "thread", TurnID: "turn"}, "")
	pending, err := db.BeginExecutionOperation(context.Background(), run.ID, run.Revision, "turn/interrupt")
	if err != nil {
		t.Fatal(err)
	}
	reconnected, err := OpenExecutionRunDatabase(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	loaded, err := reconnected.ReadExecutionRun(context.Background(), run.ID)
	if err != nil || loaded.PendingOperation != "turn/interrupt" || loaded.PendingRevision != pending.PendingRevision {
		t.Fatalf("reconnect lost interrupt intent: %#v %v", loaded, err)
	}
	stopped := updateRun(t, reconnected, loaded, storage.RunProgress{Phase: "implementation", State: "interrupted", Outcome: "cancelled", ThreadID: "thread", TurnID: "turn", Summary: "Turn stopped before request response"}, "")
	if count, err := reconnected.PruneRunActivity(context.Background(), time.Now().Add(time.Hour), 1); err != nil || count != 0 {
		t.Fatalf("unresolved operation evidence pruned: %d %v", count, err)
	}
	capture := stopped.RunCapture
	capture.ResumeFrom = &stopped.ID
	if _, err := reconnected.CreateExecutionRun(context.Background(), capture); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("pending operation allowed a new attempt: %v", err)
	}
	reconciled, err := reconnected.FinishExecutionOperation(context.Background(), storage.ExecutionOperationResult{ID: run.ID, PendingRevision: pending.PendingRevision})
	if err != nil || reconciled.PendingOperation != "" || reconciled.State != stopped.State || reconciled.Outcome != stopped.Outcome || reconciled.Summary != stopped.Summary || !reconciled.FinishedAt.Equal(*stopped.FinishedAt) {
		t.Fatalf("response overwrote terminal progress: %#v %v", reconciled, err)
	}
	if _, err := reconnected.FinishExecutionOperation(context.Background(), storage.ExecutionOperationResult{ID: run.ID, PendingRevision: pending.PendingRevision}); domain.PublicError(err).Code != "execution_run_conflict" {
		t.Fatalf("response replay accepted: %v", err)
	}
	if _, err := reconnected.CreateExecutionRun(context.Background(), capture); err != nil {
		t.Fatal(err)
	}
}
