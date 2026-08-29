package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"sort"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestMemoryRepositoryProvenanceProjectScopeListAndShow(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	agent, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{
		Text: "code-123 is ordinary text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID <= 0 || agent.ProjectID != fixture.main.Project.ID || agent.ProjectCode != "code" || agent.CreatedBy != domain.MemoryCreatedByAgent || agent.ApprovedAt != nil {
		t.Fatalf("agent memory = %#v", agent)
	}
	if !agent.CreatedAt.Equal(agent.UpdatedAt) {
		t.Fatalf("agent creation timestamps differ: %#v", agent)
	}

	human, err := repository.CreateMemory(context.Background(), fixture.linked.Project, storage.NewMemory{
		Text: "reviewed human fact", CreatedBy: domain.MemoryCreatedByHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	if human.CreatedBy != domain.MemoryCreatedByHuman || human.ApprovedAt == nil || !human.ApprovedAt.Equal(human.CreatedAt) || !human.UpdatedAt.Equal(human.CreatedAt) {
		t.Fatalf("human memory provenance/timestamps = %#v", human)
	}
	other, err := repository.CreateMemory(context.Background(), fixture.other.Project, storage.NewMemory{Text: "other project fact"})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == agent.ID || other.ID == human.ID {
		t.Fatalf("database-local memory IDs were reused: %d, %d, %d", agent.ID, human.ID, other.ID)
	}

	listed, err := repository.ListMemories(context.Background(), fixture.linked.Project, storage.MemoryListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != human.ID || listed[1].ID != agent.ID {
		t.Fatalf("project memory list = %#v", listed)
	}
	approved, err := repository.ListMemories(context.Background(), fixture.main.Project, storage.MemoryListOptions{ApprovedOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(approved) != 1 || !reflect.DeepEqual(approved[0], human) {
		t.Fatalf("approved memory list = %#v, want %#v", approved, human)
	}
	limit := int64(1)
	limited, err := repository.ListMemories(context.Background(), fixture.main.Project, storage.MemoryListOptions{Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].ID != human.ID {
		t.Fatalf("limited memory list = %#v", limited)
	}

	shown, err := repository.ReadMemory(context.Background(), fixture.linked.Project, agent.ID)
	if err != nil || !reflect.DeepEqual(shown, agent) {
		t.Fatalf("linked-worktree ReadMemory() = (%#v, %v), want %#v", shown, err, agent)
	}
	if _, err := repository.ReadMemory(context.Background(), fixture.main.Project, other.ID); err == nil || domain.PublicError(err).Code != "memory_not_found" {
		t.Fatalf("cross-project ReadMemory() error = %v", err)
	}

	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM memories_fts", 3)
	assertPelletQueryInt(t, repository.db, `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH '"code-123"'`, 1)
}

func TestMemoryRepositoryApprovalIsIdempotentAndPreservesFirstTimestamp(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	created, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "approval fact"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE memories
		SET created_at = julianday('now', '-1 day'), updated_at = julianday('now', '-1 day')
		WHERE memory_id = ?`, created.ID); err != nil {
		t.Fatal(err)
	}
	before, err := repository.ReadMemory(context.Background(), fixture.main.Project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.ApproveMemory(context.Background(), fixture.main.Project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ApprovedAt == nil || !first.UpdatedAt.Equal(*first.ApprovedAt) || !first.UpdatedAt.After(before.UpdatedAt) || !first.CreatedAt.Equal(before.CreatedAt) || first.Text != before.Text || first.CreatedBy != before.CreatedBy {
		t.Fatalf("first approval = %#v, before %#v", first, before)
	}
	repeated, err := repository.ApproveMemory(context.Background(), fixture.linked.Project, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repeated, first) {
		t.Fatalf("repeated approval changed memory:\nfirst=%#v\nrepeat=%#v", first, repeated)
	}
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM memories_fts", 1)

	human, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{
		Text: "already reviewed", CreatedBy: domain.MemoryCreatedByHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	repeatedHuman, err := repository.ApproveMemory(context.Background(), fixture.main.Project, human.ID)
	if err != nil || !reflect.DeepEqual(repeatedHuman, human) {
		t.Fatalf("human approval repeat = (%#v, %v), want %#v", repeatedHuman, err, human)
	}
}

func TestMemoryRepositoryFTSFailureRollsBackAuthoritativeCreation(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()
	if _, err := repository.db.Exec("DROP TABLE memories_fts"); err != nil {
		t.Fatal(err)
	}
	_, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "must roll back"})
	if err == nil || domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("CreateMemory() error = %v", err)
	}
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM memories", 0)
}

func TestPelletLifecycleAndPurgeNeverMutateMemory(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	memoryRepository := openMemoryRepositoryFixture(t, fixture.path)
	memory, err := memoryRepository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "code-1 lifecycle context"})
	if err != nil {
		t.Fatal(err)
	}
	before := captureAuthoritativeMemoryState(t, memoryRepository.db)
	if err := memoryRepository.Close(); err != nil {
		t.Fatal(err)
	}

	pellets := fixture.open(t)
	created, err := pellets.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "lifecycle subject"})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []storage.PelletLifecycleOperation{
		storage.PelletStart,
		storage.PelletRelease,
		storage.PelletStart,
		storage.PelletClose,
		storage.PelletReopen,
		storage.PelletDefer,
		storage.PelletReopen,
		storage.PelletClose,
	} {
		if _, err := pellets.TransitionPellet(context.Background(), fixture.main, created.Reference, storage.PelletLifecycleRequest{Operation: operation}); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
	}
	removed, err := pellets.PurgeClosedPellets(context.Background(), fixture.main.Project, storage.PelletPurgeOptions{})
	if err != nil || len(removed) != 1 || removed[0] != created.Reference {
		t.Fatalf("PurgeClosedPellets() = (%v, %v)", removed, err)
	}
	if err := pellets.Close(); err != nil {
		t.Fatal(err)
	}

	memoryRepository = openMemoryRepositoryFixture(t, fixture.path)
	defer memoryRepository.Close()
	after := captureAuthoritativeMemoryState(t, memoryRepository.db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("pellet lifecycle/purge mutated memory:\nbefore=%v\nafter=%v", before, after)
	}
	shown, err := memoryRepository.ReadMemory(context.Background(), fixture.main.Project, memory.ID)
	if err != nil || shown.Text != memory.Text {
		t.Fatalf("memory after pellet purge = (%#v, %v)", shown, err)
	}
}

func TestMemorySchemaHasNoPelletLifecycleOrGroupingColumns(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()
	rows, err := repository.db.Query("PRAGMA table_info(memories)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"memory_id", "project_id", "text", "created_by", "approved_at", "created_at", "updated_at"}
	if !reflect.DeepEqual(columns, want) {
		t.Fatalf("memory schema columns = %v, want %v", columns, want)
	}
}

func openMemoryRepositoryFixture(t *testing.T, path string) *MemoryRepository {
	t.Helper()
	repository, err := OpenMemoryRepository(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func captureAuthoritativeMemoryState(t *testing.T, database *sql.DB) []string {
	t.Helper()
	rows, err := database.Query(`
		SELECT printf('%d|%d|%s|%s|%s|%.12f|%.12f',
		       memory_id, project_id, quote(text), created_by,
		       coalesce(printf('%.12f', approved_at), 'NULL'), created_at, updated_at)
		FROM memories
		UNION ALL
		SELECT printf('fts|%d|%s', rowid, quote(text)) FROM memories_fts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var state []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		state = append(state, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(state)
	return state
}
