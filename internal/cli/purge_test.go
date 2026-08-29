package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

func TestPurgeCommandDryRunAndConfirmedDeletion(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "purge database root 界")
	mainRoot := filepath.Join(common, "main")
	otherRoot := filepath.Join(common, "other")
	for _, root := range []string{mainRoot, otherRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, root, "init", "--quiet")
	}
	if stdout, stderr, exit := runTestApp(initDBTestApp(common), "init-db"); exit != 0 || stderr != "" {
		t.Fatalf("init-db = exit %d stdout %q stderr %q", exit, stdout, stderr)
	}

	current := mainRoot
	application := projectTestApp(&current)
	runProjectInit(t, application, "main")
	current = otherRoot
	runProjectInit(t, application, "other")

	current = mainRoot
	for _, title := range []string{"old closed", "boundary closed", "recent closed"} {
		raw := runPelletCommand(t, application, "add", title)
		var added struct {
			Data pelletData `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &added); err != nil {
			t.Fatal(err)
		}
		runPelletCommand(t, application, "close", added.Data.ID)
	}
	runPelletCommand(t, application, "add", "open survivor")
	runPelletCommand(t, application, "add", "in progress survivor")
	runPelletCommand(t, application, "start", "main-5")
	runPelletCommand(t, application, "add", "deferred survivor", "--maybe-later")
	memory := decodeMemoryCommand(
		t,
		runPelletCommand(t, application, "memory", "add", "--text", "main-1 remains ordinary memory text"),
		"memory add",
	)

	current = otherRoot
	runPelletCommand(t, application, "add", "other closed survivor")
	runPelletCommand(t, application, "close", "other-1")

	databasePath := discovery.DatabasePath(common)
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for number, completed := range map[int]string{
		1: "2029-12-31T23:59:59Z",
		2: "2030-01-01T00:00:00Z",
		3: "2031-01-01T00:00:00Z",
	} {
		if _, err := database.Exec(`
			UPDATE pellets
			SET completed_at = julianday(?)
			WHERE project_id = (SELECT project_id FROM projects WHERE code = 'main') AND number = ?`, completed, number); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	// Purge is a database-level administrative command: it does not require a
	// current Git repository, but it always requires an explicit project code.
	current = common
	beforePreview := capturePurgeDatabaseState(t, databasePath)
	cutoffPreviewRaw := runPelletCommand(
		t, application, "purge", "--project", "main", "--closed-before", "2030-01-01", "--dry-run",
	)
	cutoffPreview := decodePurgeCommand(t, cutoffPreviewRaw)
	if !cutoffPreview.DryRun || cutoffPreview.Project != "main" || cutoffPreview.Count != 1 || !reflect.DeepEqual(cutoffPreview.References, []string{"main-1"}) {
		t.Fatalf("cutoff dry-run = %#v", cutoffPreview)
	}
	if afterPreview := capturePurgeDatabaseState(t, databasePath); !reflect.DeepEqual(afterPreview, beforePreview) {
		t.Fatalf("dry-run changed database state:\nbefore=%q\nafter=%q", beforePreview, afterPreview)
	}

	allPreviewRaw := runPelletCommand(t, application, "--project", "main", "purge", "--dry-run")
	allPreview := decodePurgeCommand(t, allPreviewRaw)
	if !reflect.DeepEqual(allPreview.References, []string{"main-1", "main-2", "main-3"}) || allPreview.Count != 3 {
		t.Fatalf("all-closed dry-run = %#v", allPreview)
	}

	human, stderr, exit := runTestApp(
		application, "--human", "purge", "--project", "main", "--closed-before", "2030-01-01", "--dry-run",
	)
	if exit != 0 || stderr != "" || human != "Would purge 1 closed pellet from main: main-1\n" {
		t.Fatalf("human purge preview = exit %d stdout %q stderr %q", exit, human, stderr)
	}

	confirmedCutoffRaw := runPelletCommand(
		t, application, "purge", "--project", "main", "--closed-before", "2030-01-01T00:00:00Z", "--yes",
	)
	confirmedCutoff := decodePurgeCommand(t, confirmedCutoffRaw)
	if confirmedCutoff.DryRun || !reflect.DeepEqual(confirmedCutoff.References, []string{"main-1"}) {
		t.Fatalf("confirmed cutoff purge = %#v", confirmedCutoff)
	}

	confirmedAllRaw := runPelletCommand(t, application, "--project", "main", "purge", "--yes")
	confirmedAll := decodePurgeCommand(t, confirmedAllRaw)
	if confirmedAll.DryRun || !reflect.DeepEqual(confirmedAll.References, []string{"main-2", "main-3"}) {
		t.Fatalf("confirmed all purge = %#v", confirmedAll)
	}

	database, err = sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	assertPurgeQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE project_id = (SELECT project_id FROM projects WHERE code = 'main')`, 3)
	assertPurgeQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE project_id = (SELECT project_id FROM projects WHERE code = 'main')
		  AND status IN ('open', 'in_progress', 'maybe_later')`, 3)
	assertPurgeQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE project_id = (SELECT project_id FROM projects WHERE code = 'main')
		  AND status = 'closed'`, 0)
	assertPurgeQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE project_id = (SELECT project_id FROM projects WHERE code = 'other')
		  AND status = 'closed'`, 1)
	assertPurgeQueryInt(t, database, `SELECT COUNT(*) FROM pellets_fts`, 4)
	assertPurgeQueryInt(t, database, `
		SELECT COUNT(*) FROM memories
		WHERE project_id = (SELECT project_id FROM projects WHERE code = 'main')`, 1)
	assertPurgeQueryInt(t, database, `SELECT COUNT(*) FROM memories_fts`, 1)
	assertPurgeQueryInt(t, database, `
		SELECT next_pellet_number FROM projects WHERE code = 'main'`, 7)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database = nil

	current = mainRoot
	shownMemory := decodeMemoryCommand(t, runPelletCommand(t, application, "memory", "show", "1"), "memory show")
	if !reflect.DeepEqual(shownMemory, memory) {
		t.Fatalf("memory after purge = %#v, want %#v", shownMemory, memory)
	}
	postPurge := runPelletCommand(t, application, "add", "number after purge")
	if !strings.Contains(postPurge, `"id":"main-7"`) || !strings.Contains(postPurge, `"number":7`) {
		t.Fatalf("post-purge allocation reused a number: %s", postPurge)
	}

	var golden strings.Builder
	appendPelletGolden(t, &golden, "dry-run-cutoff", cutoffPreviewRaw)
	appendPelletGolden(t, &golden, "dry-run-all", allPreviewRaw)
	appendPelletGolden(t, &golden, "confirmed-cutoff", confirmedCutoffRaw)
	appendPelletGolden(t, &golden, "confirmed-all", confirmedAllRaw)
	assertGolden(t, "purge.golden", golden.String())
}

func TestPurgeCommandValidationIsStrictAndSideEffectFree(t *testing.T) {
	t.Parallel()

	application := New("test", PurgeCommand(emptyPelletManager()))
	workingDirectoryCalls := 0
	application.workingDirectory = func() (string, error) {
		workingDirectoryCalls++
		return "", fmt.Errorf("purge usage validation crossed discovery boundary")
	}
	for _, test := range []struct {
		name     string
		args     []string
		code     string
		wantExit int
	}{
		{name: "missing explicit project", args: []string{"purge", "--dry-run"}, code: "missing_required_flag", wantExit: 2},
		{name: "missing confirmation mode", args: []string{"--project", "main", "purge"}, code: "confirmation_required", wantExit: 6},
		{name: "both confirmation modes", args: []string{"--project", "main", "purge", "--dry-run", "--yes"}, code: "conflicting_flags", wantExit: 2},
		{name: "project supplied twice", args: []string{"--project", "main", "purge", "--project", "main", "--dry-run"}, code: "conflicting_project_selection", wantExit: 2},
		{name: "malformed project", args: []string{"purge", "--project", "bad_code", "--dry-run"}, code: "invalid_project_code", wantExit: 2},
		{name: "malformed cutoff", args: []string{"purge", "--project", "main", "--closed-before", "01/02/2030", "--dry-run"}, code: "invalid_date", wantExit: 2},
		{name: "cutoff missing value", args: []string{"purge", "--project", "main", "--closed-before", "--dry-run"}, code: "missing_flag_value", wantExit: 2},
		{name: "confirmation value", args: []string{"purge", "--project", "main", "--yes=true"}, code: "unexpected_flag_value", wantExit: 2},
		{name: "positional argument", args: []string{"purge", "--project", "main", "--dry-run", "extra"}, code: "unexpected_argument", wantExit: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, exit := runTestApp(application, test.args...)
			if exit != test.wantExit || stdout != "" || !strings.Contains(stderr, `"code":"`+test.code+`"`) {
				t.Fatalf("%v = exit %d stdout %q stderr %q, want exit %d code %s", test.args, exit, stdout, stderr, test.wantExit, test.code)
			}
		})
	}
	if workingDirectoryCalls != 0 {
		t.Fatalf("invalid purge commands crossed working-directory boundary %d times", workingDirectoryCalls)
	}
}

func decodePurgeCommand(t *testing.T, raw string) purgeData {
	t.Helper()
	var envelope struct {
		SchemaVersion int       `json:"schema_version"`
		Command       string    `json:"command"`
		Data          purgeData `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode purge output %q: %v", raw, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != "purge" || envelope.Data.References == nil {
		t.Fatalf("purge envelope = %#v", envelope)
	}
	return envelope.Data
}

func capturePurgeDatabaseState(t *testing.T, path string) []string {
	t.Helper()
	state := capturePelletLogicalState(t, path)
	database, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, query := range []string{
		`SELECT 'pellet-fts|' || quote(rowid) || '|' || quote(title) || '|' || quote(description) || '|' || quote(external_id) FROM pellets_fts ORDER BY rowid`,
		`SELECT 'memory|' || quote(memory_id) || '|' || quote(project_id) || '|' || quote(text) || '|' || quote(created_by) || '|' || quote(approved_at) || '|' || quote(created_at) || '|' || quote(updated_at) FROM memories ORDER BY memory_id`,
		`SELECT 'memory-fts|' || quote(rowid) || '|' || quote(text) FROM memories_fts ORDER BY rowid`,
	} {
		rows, err := database.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			state = append(state, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func assertPurgeQueryInt(t *testing.T, database *sql.DB, query string, want int, arguments ...any) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(context.Background(), query, arguments...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query result = %d, want %d; query: %s", got, want, query)
	}
}
