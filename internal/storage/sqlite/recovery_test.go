package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"pellets/internal/storage"
)

func TestRecoveryMigrationRollbackAndSavedIntent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "recovery.db")
	old, err := openWithMigrations(ctx, path, migrations[:10])
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	sequence := append([]migration(nil), migrations...)
	sequence[10].assert = func(context.Context, *sql.Conn) error { return errors.New("injected recovery migration write failure") }
	if db, err := openWithMigrations(ctx, path, sequence); err == nil || db != nil {
		t.Fatal("failed recovery migration committed")
	}
	raw := openRawDatabase(t, path)
	assertPragmaInt(t, raw, "user_version", 10)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM pragma_table_info('execution_runs') WHERE name='starting_ref'`, 0)
	raw.Close()
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	assertPragmaInt(t, migrated, "user_version", 11)
	migrated.Close()

	db, run, _ := createTestRun(t)
	// Exact schedule intent and branch are immutable even if a Resume caller
	// sends a different mode, branch, limit, or starting commit.
	run = updateRun(t, db, run, storage.RunProgress{Phase: "review", State: "interrupted", Outcome: "unknown", ThreadID: "saved-thread", TurnID: "saved-turn"}, "")
	capture := run.RunCapture
	capture.ResumeFrom = &run.ID
	capture.StartingRef, capture.ScheduleMode, capture.ScheduleRemaining = "refs/heads/replacement", "watch", 999
	resumed, err := db.CreateExecutionRun(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.StartingRef != run.StartingRef || resumed.ScheduleMode != run.ScheduleMode || resumed.ScheduleRemaining != run.ScheduleRemaining || resumed.Phase != "review" || resumed.TurnID != "saved-turn" {
		t.Fatalf("resume changed saved intent: %+v", resumed)
	}
}
