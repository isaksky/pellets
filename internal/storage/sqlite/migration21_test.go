package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"pellets/internal/storage"
)

// Capture raw columns so no newly added model field can normalize old evidence.
func groupMigrationRows(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func TestProjectGroupsMigrationPreservesNamesMembershipAndHistoricalEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v20.db")
	db, err := openWithMigrations(ctx, path, migrations[:20])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, db, 1, 1, "one", "one")
	mustInsertProjectAndWorkspace(t, db, 2, 2, "two", "two")
	complete := float64(1)
	mustInsertPellet(t, db, 1, 1, "open", nil, intPtr(1024), nil)
	mustInsertPellet(t, db, 1, 2, "in_progress", intPtr(1), intPtr(2048), nil)
	mustInsertPellet(t, db, 1, 3, "closed", nil, nil, &complete)
	mustInsertPellet(t, db, 1, 4, "maybe_later", nil, nil, nil)
	mustInsertPellet(t, db, 1, 5, "open", nil, intPtr(3072), nil)
	mustInsertPellet(t, db, 2, 1, "open", nil, intPtr(1024), nil)
	mustExec(t, db, `UPDATE pellets SET group_id=CASE WHEN number=4 THEN 'case' ELSE 'Case' END WHERE number<>5;
 INSERT INTO pellets(project_id,number,title,kind,priority,created_at,updated_at) VALUES(1,6,'review','review_checkpoint',4096,1,1);
 INSERT INTO review_checkpoint_targets(project_id,checkpoint_number,target_number,ordinal,selected_reference,title,description,group_id)
 VALUES(1,6,3,0,'one-3','pellet','','Case');
 INSERT INTO workspace_group_assignments(workspace_id,project_id,mode,include_ungrouped,groups_json)
 VALUES(1,1,'explicit',1,'["Case"," routing-only Ω\\n ","Case"]'),(2,2,'remaining',0,'["Case","routing-only"]');
 INSERT INTO pellet_add_requests VALUES(1,'old-request','original-fingerprint','{"Group":"Case"}',2460000);
 INSERT INTO planning_chats(project_id,request_id,request_fingerprint,revision,state_json,created_at,updated_at)
 VALUES(1,'old-chat',printf('%064d',0),1,'{"drafts":[{"id":"draft","title":"Saved draft","group":"draft-only"}]}',2460000,2460000);
 INSERT INTO planning_draft_creations VALUES(1,1,'created',3,'{"group":"Case"}','{"Group":"Case"}');
 INSERT INTO pellets_fts(rowid,title,description,external_id) SELECT rowid,title,description,external_id FROM pellets;`)
	capture := runCapture(1, 1, 2)
	group := "Case"
	capture.Group = &group
	capture.WorkspaceSelection = &storage.WorkspaceSelection{Enabled: true, Mode: "explicit", Groups: []string{group}}
	insertLegacyContextRun(t, db, capture)
	// Snapshot every old pellet column and every populated immutable record.
	queries := []string{
		`SELECT project_id,number,group_id,status,workspace_id,priority,created_at,updated_at,completed_at,implementation_revision FROM pellets ORDER BY project_id,number`,
		legacyExecutionProjection(t, db), `SELECT * FROM execution_run_activity`, `SELECT * FROM review_checkpoint_targets`,
		`SELECT * FROM pellet_add_requests`, `SELECT * FROM planning_chats`, `SELECT * FROM planning_draft_creations`,
		`SELECT * FROM workspace_group_assignments`,
	}
	before := make([]string, len(queries))
	for i, q := range queries {
		before[i] = groupMigrationRows(t, db, q)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDatabase(t, path)
	defer db.Close()
	for i, q := range queries {
		if got := groupMigrationRows(t, db, q); got != before[i] {
			t.Fatalf("migration rewrote %s\nbefore=%s\nafter=%s", q, before[i], got)
		}
	}
	assertQueryInt(t, db, `SELECT COUNT(*) FROM groups`, 5)
	assertQueryInt(t, db, `SELECT COUNT(*) FROM groups WHERE context='' AND revision=1`, 5)
	assertQueryInt(t, db, `SELECT COUNT(DISTINCT group_id) FROM groups WHERE name='Case'`, 2)
	assertQueryInt(t, db, `SELECT COUNT(*) FROM groups WHERE name='draft-only'`, 0)
	assertQueryInt(t, db, `SELECT COUNT(*) FROM pellets p JOIN groups g ON p.group_record_id=g.group_id WHERE p.project_id=g.project_id AND p.group_id=g.name`, 5)
	assertQueryInt(t, db, `SELECT COUNT(*) FROM pellets WHERE group_id IS NULL AND group_record_id IS NULL`, 2)
	assertQueryInt(t, db, `SELECT COUNT(*) FROM pragma_foreign_key_check`, 0)
	var routingName string
	if err = db.QueryRow(`SELECT name FROM groups WHERE project_id=1 AND name LIKE ' routing-only%'`).Scan(&routingName); err != nil {
		t.Fatal(err)
	}
	if routingName != " routing-only Ω\\n " {
		t.Fatalf("name bytes changed: %q", routingName)
	}
}

func TestProjectGroupsMigrationFailureRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rollback.db")
	db, err := openWithMigrations(ctx, path, migrations[:20])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, db, 1, 1, "one", "one")
	mustInsertPellet(t, db, 1, 1, "open", nil, intPtr(1024), nil)
	mustExec(t, db, `UPDATE pellets SET group_id='unchanged'; INSERT INTO pellets_fts(rowid,title,description,external_id) SELECT rowid,title,description,external_id FROM pellets`)
	before := groupMigrationRows(t, db, "SELECT * FROM pellets")
	db.Close()
	sequence := append([]migration{}, migrations...)
	sequence[20].assert = func(context.Context, *sql.Conn) error { return errors.New("injected failure") }
	migrated, err := openWithMigrations(ctx, path, sequence)
	if migrated != nil {
		migrated.Close()
		t.Fatal("failed migration opened database")
	}
	assertDomainErrorCode(t, err, "database_migration_failed")
	raw := openRawDatabase(t, path)
	defer raw.Close()
	assertPragmaInt(t, raw, "user_version", 20)
	assertQueryInt(t, raw, `SELECT COUNT(*) FROM sqlite_schema WHERE name='groups'`, 0)
	if !reflect.DeepEqual(before, groupMigrationRows(t, raw, "SELECT * FROM pellets")) {
		t.Fatal("failed migration changed pellets")
	}
}
