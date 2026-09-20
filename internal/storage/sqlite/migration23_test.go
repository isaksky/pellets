package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCheckpointContextDigestMigrationPreservesLegacyReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v22.db")
	db, err := openWithMigrations(ctx, path, migrations[:22])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, db, 1, 1, "one", "one")
	mustExec(t, db, `INSERT INTO checkpoint_triage VALUES(1,7,2,'{"version":1,"status":"clean","summary":"clean","findings":[]}','review-thread','review-turn')`)
	before := groupMigrationRows(t, db, `SELECT project_id,checkpoint_number,implementation_revision,review_result_json,review_thread_id,review_turn_id FROM checkpoint_triage`)
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after := groupMigrationRows(t, db, `SELECT project_id,checkpoint_number,implementation_revision,review_result_json,review_thread_id,review_turn_id FROM checkpoint_triage`)
	if before != after {
		t.Fatal("migration rewrote historical receipt")
	}
	var digest string
	if err := db.QueryRow(`SELECT review_snapshot_sha256 FROM checkpoint_triage`).Scan(&digest); err != nil || digest != "" {
		t.Fatalf("legacy receipt invented digest: %q %v", digest, err)
	}
	assertPragmaInt(t, db, "user_version", LatestSchemaVersion)
}
