package main

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"pellets/internal/storage/sqlite"
)

func TestReleaseContradictionChecklist(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	plan := readReleaseContractFile(t, filepath.Join(repositoryRoot, "docs", "implementation-plan.md"))
	checklistStart := strings.Index(plan, "## Contradiction checklist\n")
	if checklistStart < 0 {
		t.Fatal("docs/implementation-plan.md has no release contradiction checklist")
	}
	checklist := plan[checklistStart:]
	bullets := 0
	for _, line := range strings.Split(checklist, "\n") {
		if strings.HasPrefix(line, "- ") {
			bullets++
		}
	}
	if bullets != 14 {
		t.Fatalf("release contradiction checklist has %d items, want 14 explicit contracts", bullets)
	}
	for _, contract := range []string{
		"no schema, command, or prose introduces dependency concepts",
		"canonical project codes and direct redirects remain unambiguous",
		"no priority path uses floating-point arithmetic or a linked list",
		"non-null priority remains unique per project",
		"group is one optional exact-filter value per pellet",
		"`next` is read-only",
		"pellet numbers are project-local",
		"one logical repository has shared project state across worktrees",
		"no schema or prose invents an agent/PID/session/lease/heartbeat/expiry ownership model",
		"memory has no task foreign key and uses FTS5 only",
		"no core behavior needs external network access or a vector capability",
		"the database is never part of a Git synchronization workflow",
		"JSON v1 fixtures and exit codes match",
	} {
		if !strings.Contains(checklist, contract) {
			t.Errorf("release contradiction checklist is missing %q", contract)
		}
	}

	databasePath := filepath.Join(t.TempDir(), "release-checklist.db")
	database, err := sqlite.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	pelletColumns := releaseTableColumns(t, database, "pellets")
	memoryColumns := releaseTableColumns(t, database, "memories")
	wantPelletColumns := []string{
		"rowid:INTEGER", "project_id:INTEGER", "workspace_id:INTEGER", "number:INTEGER", "title:TEXT",
		"description:TEXT", "external_id:TEXT", "group_id:TEXT", "status:TEXT", "priority:INTEGER",
		"created_at:REAL", "updated_at:REAL", "completed_at:REAL",
	}
	wantMemoryColumns := []string{
		"memory_id:INTEGER", "project_id:INTEGER", "text:TEXT", "created_by:TEXT", "approved_at:REAL",
		"created_at:REAL", "updated_at:REAL",
	}
	if strings.Join(pelletColumns, ",") != strings.Join(wantPelletColumns, ",") {
		t.Fatalf("pellet schema contradicts the release model:\n got %v\nwant %v", pelletColumns, wantPelletColumns)
	}
	if strings.Join(memoryColumns, ",") != strings.Join(wantMemoryColumns, ",") {
		t.Fatalf("memory schema contradicts the release model:\n got %v\nwant %v", memoryColumns, wantMemoryColumns)
	}

	var priorityIndex string
	if err := database.QueryRow("SELECT lower(sql) FROM sqlite_schema WHERE type = 'index' AND name = 'pellets_active_priority_idx'").Scan(&priorityIndex); err != nil {
		t.Fatal(err)
	}
	priorityIndex = strings.Join(strings.Fields(priorityIndex), " ")
	if !strings.Contains(priorityIndex, "create unique index") ||
		!strings.Contains(priorityIndex, "on pellets(project_id, priority)") ||
		!strings.Contains(priorityIndex, "where priority is not null") {
		t.Fatalf("active-priority index is not project-scoped integer uniqueness: %s", priorityIndex)
	}

	var pelletSchema string
	if err := database.QueryRow("SELECT lower(sql) FROM sqlite_schema WHERE type = 'table' AND name = 'pellets'").Scan(&pelletSchema); err != nil {
		t.Fatal(err)
	}
	pelletSchema = strings.Join(strings.Fields(pelletSchema), " ")
	for _, invariant := range []string{
		"status in ('open', 'in_progress', 'closed', 'maybe_later')",
		"status in ('open', 'in_progress') and priority is not null and priority > 0",
		"status in ('closed', 'maybe_later') and priority is null",
		"status = 'in_progress' and workspace_id is not null",
		"status <> 'in_progress' and workspace_id is null",
	} {
		if !strings.Contains(pelletSchema, invariant) {
			t.Errorf("pellet schema lacks release invariant %q: %s", invariant, pelletSchema)
		}
	}

	forbiddenTables := []string{"agents", "claims", "dependencies", "dependency_edges", "events", "groups", "heartbeats", "leases", "sessions", "tags", "vectors"}
	for _, table := range forbiddenTables {
		assertFoundationQueryInt(t, database, "SELECT COUNT(*) FROM sqlite_schema WHERE lower(name) = ?", 0, table)
	}

	foreignKeys, err := database.Query("PRAGMA foreign_key_list(memories)")
	if err != nil {
		t.Fatal(err)
	}
	defer foreignKeys.Close()
	foreignKeyCount := 0
	for foreignKeys.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := foreignKeys.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		foreignKeyCount++
		if table != "projects" || from != "project_id" || to != "project_id" {
			t.Fatalf("memory has a non-project foreign key: table=%s from=%s to=%s", table, from, to)
		}
	}
	if err := foreignKeys.Err(); err != nil {
		t.Fatal(err)
	}
	if foreignKeyCount != 1 {
		t.Fatalf("memory foreign keys = %d, want only project ownership", foreignKeyCount)
	}
	var memoryFTSSchema string
	if err := database.QueryRow("SELECT lower(sql) FROM sqlite_schema WHERE type = 'table' AND name = 'memories_fts'").Scan(&memoryFTSSchema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(memoryFTSSchema, "using fts5") || !strings.Contains(memoryFTSSchema, "content = 'memories'") || strings.Contains(memoryFTSSchema, "vector") || strings.Contains(memoryFTSSchema, "embedding") {
		t.Fatalf("memory search schema contradicts FTS5-only contract: %s", memoryFTSSchema)
	}

	cliSpec := readReleaseContractFile(t, filepath.Join(repositoryRoot, "docs", "cli-spec.md"))
	for _, absent := range []string{"`block`", "`dependency`", "`graph`", "`claim`", "`assign`", "`sync`"} {
		if !strings.Contains(cliSpec, "There are no ") || !strings.Contains(cliSpec, absent) {
			t.Errorf("CLI specification no longer explicitly excludes command %s", absent)
		}
	}
	mainSource := readReleaseContractFile(t, filepath.Join(repositoryRoot, "cmd", "pl", "main.go"))
	for _, forbidden := range []string{"BlockCommand", "DependencyCommand", "GraphCommand", "ClaimCommand", "AssignCommand", "SyncCommand"} {
		if strings.Contains(mainSource, forbidden) {
			t.Errorf("compiled command registration introduces forbidden release concept %s", forbidden)
		}
	}

	forbiddenNetworkClients := []string{
		"http.Get(", "http.Post(", "http.DefaultClient", "http.Client{", "net.Dial(", "net.Dialer{", "tls.Dial(", "websocket",
	}
	err = filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != repositoryRoot && (entry.Name() == ".git" || entry.Name() == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range forbiddenNetworkClients {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("production source %s contains external network client token %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	prioritySources := []string{
		readReleaseContractFile(t, filepath.Join(repositoryRoot, "internal", "storage", "pellet.go")),
		readReleaseContractFile(t, filepath.Join(repositoryRoot, "internal", "storage", "sqlite", "pellets.go")),
	}
	for _, source := range prioritySources {
		for _, line := range strings.Split(source, "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "priority") && (strings.Contains(lower, "float32") || strings.Contains(lower, "float64")) {
				t.Errorf("priority path uses floating-point arithmetic: %s", strings.TrimSpace(line))
			}
			if strings.Contains(lower, "priority") && (strings.Contains(lower, "next_priority") || strings.Contains(lower, "previous_priority")) {
				t.Errorf("priority path introduces a linked-list field: %s", strings.TrimSpace(line))
			}
		}
	}
}

func TestWindowsReleaseSmokeUsesAutomaticallyGeneratedPelletIdentity(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workflow := readReleaseContractFile(t, filepath.Join(repositoryRoot, ".github", "workflows", "foundation.yml"))
	for _, required := range []string{
		`$started.data.pellet.id -ne $added.data.id`,
		`$executable close $added.data.id`,
		`$closed.data.id -ne $added.data.id`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("Windows release smoke does not preserve the generated pellet identity through %q", required)
		}
	}
	if strings.Contains(workflow, `$executable close smoke-1`) {
		t.Error("Windows release smoke hard-codes a project code instead of using the first-use bootstrap result")
	}
}

func releaseTableColumns(t *testing.T, database interface {
	Query(string, ...any) (*sql.Rows, error)
}, table string) []string {
	t.Helper()
	rows, err := database.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make([]struct {
		position int
		value    string
	}, 0)
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, declaredType string
		var defaultValue any
		if err := rows.Scan(&position, &name, &declaredType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, struct {
			position int
			value    string
		}{position: position, value: name + ":" + strings.ToUpper(declaredType)})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Slice(columns, func(left, right int) bool { return columns[left].position < columns[right].position })
	values := make([]string, len(columns))
	for index, column := range columns {
		values[index] = column.value
	}
	return values
}

func readReleaseContractFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
