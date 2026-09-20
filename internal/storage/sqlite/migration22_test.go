package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"pellets/internal/storage"
)

// Write a pre-capture fixture using its historical columns, not today's API.
func insertLegacyContextRun(t *testing.T, db *sql.DB, capture storage.RunCapture) {
	t.Helper()
	settings, _ := json.Marshal(capture.Settings)
	prefix, _ := json.Marshal(capture.PromptPrefix)
	routing, _ := json.Marshal(capture.WorkspaceSelection)
	_, err := db.Exec(`INSERT INTO execution_runs(project_id,workspace_id,pellet_number,attempt,mode,settings_json,prompt_prefix_json,starting_head,external_id,group_id,workspace_selection_json,pellet_title,pellet_description,phase,state,outcome,revision,created_at,updated_at,finished_at,implementation_revision)
 SELECT project_id,workspace_id,number,1,'run_one',?,?,?,?,?,?,title,description,'implementation','interrupted','unknown',1,'2026-01-01T00:00:00.000000000Z','2026-01-01T00:00:00.000000000Z','2026-01-01T00:00:00.000000000Z',implementation_revision FROM pellets WHERE project_id=? AND number=?`, string(settings), string(prefix), capture.StartingHead, capture.ExternalID, capture.Group, string(routing), capture.ProjectID, capture.PelletNumber)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO execution_run_activity(run_id,revision,at,phase,state,summary,operation)
 SELECT run_id,revision,updated_at,phase,state,'Historical activity','' FROM execution_runs`)
}

func legacyExecutionProjection(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('execution_runs') ORDER BY cid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, `"`+name+`"`)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return `SELECT ` + strings.Join(columns, ",") + ` FROM execution_runs ORDER BY run_id`
}

func TestExecutionGroupContextMigrationRetainsLegacyRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v21.db")
	db, err := openWithMigrations(ctx, path, migrations[:21])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, db, 1, 1, "one", "one")
	mustInsertPellet(t, db, 1, 1, "in_progress", intPtr(1), intPtr(1024), nil)
	mustExec(t, db, `UPDATE pellets SET group_id='current'; UPDATE groups SET context='never invent this historical context'; INSERT INTO pellets_fts(rowid,title,description,external_id) SELECT rowid,title,description,external_id FROM pellets`)
	insertLegacyContextRun(t, db, runCapture(1, 1, 1))
	query := legacyExecutionProjection(t, db)
	before := groupMigrationRows(t, db, query)
	db.Close()
	repo, err := OpenExecutionRunDatabase(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if after := groupMigrationRows(t, repo.db, query); after != before {
		t.Fatal("migration changed original execution evidence")
	}
	run, err := repo.ReadExecutionRun(ctx, 1)
	if err != nil || run.GroupContext.Version != 1 || run.GroupContext.State != "legacy" || run.GroupContext.Group != nil {
		t.Fatalf("legacy snapshot: %+v %v", run.GroupContext, err)
	}
	capture := run.RunCapture
	capture.ResumeFrom = &run.ID
	resumed, err := repo.CreateExecutionRun(ctx, capture)
	if err != nil || resumed.GroupContext != run.GroupContext {
		t.Fatalf("legacy resume invented context: %+v %v", resumed.GroupContext, err)
	}
	if _, err := repo.db.Exec(`UPDATE execution_runs SET group_context_json='{"version":1,"state":"ungrouped","group":null}' WHERE run_id=1`); err == nil {
		t.Fatal("historical snapshot could be overwritten")
	}
}

func TestExecutionGroupContextMigrationRollback(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rollback.db")
	db, err := openWithMigrations(ctx, path, migrations[:21])
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	sequence := append([]migration(nil), migrations...)
	sequence[21].assert = func(context.Context, *sql.Conn) error { return errors.New("injected") }
	if _, err := openWithMigrations(ctx, path, sequence); err == nil {
		t.Fatal("failed migration committed")
	}
	db, err = openWithMigrations(ctx, path, migrations[:21])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertQueryInt(t, db, `SELECT COUNT(*) FROM pragma_table_info('execution_runs') WHERE name='group_context_json'`, 0)
}
