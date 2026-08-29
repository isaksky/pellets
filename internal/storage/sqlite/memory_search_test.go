package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestMemoryRepositorySearchSafelyEscapesTextAndPreservesTokenCharacters(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	indexed, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{
		Text: `foo-123 parser_code c++ (broken "quote literal OR NOT github:acme/tool#84`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "nonexistent only"}); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		"foo-123",
		"parser_code",
		"c++",
		"(broken",
		`"quote`,
		"OR",
		"NOT",
		"github:acme/tool#84",
	} {
		results, searchErr := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: query})
		if searchErr != nil {
			t.Fatalf("SearchMemories(%q) error = %v", query, searchErr)
		}
		assertMemoryIDs(t, results, indexed.ID)
		if results[0].Snippet == "" || results[0].Memory.Text != indexed.Text {
			t.Fatalf("SearchMemories(%q) result = %#v", query, results[0])
		}
	}

	for _, query := range []string{"(", "-", `"`, `foo-123 OR nonexistent`, `alpha) OR (*`} {
		results, searchErr := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: query})
		if searchErr != nil {
			t.Fatalf("SearchMemories(%q) treated ordinary text as syntax: %v", query, searchErr)
		}
		if len(results) != 0 {
			t.Fatalf("SearchMemories(%q) = %#v, want no literal-term match", query, results)
		}
	}
}

func TestMemoryRepositorySearchIsProjectScopedApprovedAndDeterministic(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	agent, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "sharedtoken same rank"})
	if err != nil {
		t.Fatal(err)
	}
	human, err := repository.CreateMemory(context.Background(), fixture.linked.Project, storage.NewMemory{
		Text: "sharedtoken same rank", CreatedBy: domain.MemoryCreatedByHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreateMemory(context.Background(), fixture.other.Project, storage.NewMemory{
		Text: "sharedtoken same rank", CreatedBy: domain.MemoryCreatedByHuman,
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repository.db.Exec(`
		UPDATE memories SET created_at = julianday('2030-01-02T00:00:00Z'), updated_at = julianday('2030-01-02T00:00:00Z')
		WHERE memory_id = ?`, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE memories SET created_at = julianday('2030-01-01T00:00:00Z'), updated_at = julianday('2030-01-01T00:00:00Z'),
		                    approved_at = julianday('2030-01-01T00:00:00Z')
		WHERE memory_id = ?`, human.ID); err != nil {
		t.Fatal(err)
	}

	results, err := repository.SearchMemories(context.Background(), fixture.linked.Project, storage.MemorySearchOptions{Query: "sharedtoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, results, agent.ID, human.ID)
	for _, result := range results {
		if result.Memory.ProjectID != fixture.main.Project.ID || result.Memory.ID == other.ID || result.Rank != results[0].Rank {
			t.Fatalf("project-scoped equal-rank result = %#v", result)
		}
	}

	approved, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{
		Query: "sharedtoken", ApprovedOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, approved, human.ID)
	limit := int64(1)
	limited, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{
		Query: "sharedtoken", Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, limited, agent.ID)
}

func TestMemoryRepositorySearchRanksAndHardBoundsSnippets(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	low, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "ranktoken filler words"})
	if err != nil {
		t.Fatal(err)
	}
	high, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "ranktoken ranktoken ranktoken"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "ranktoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, results, high.ID, low.ID)
	if results[0].Rank >= results[1].Rank {
		t.Fatalf("FTS ranks = %v then %v, want more relevant rank first", results[0].Rank, results[1].Rank)
	}

	longText := strings.Repeat("x", 4096) + " boundedtoken " + strings.Repeat("z", 4096)
	long, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: longText})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "boundedtoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, bounded, long.ID)
	if got := len([]rune(bounded[0].Snippet)); got > memorySnippetMaxRunes {
		t.Fatalf("snippet length = %d runes, want <= %d", got, memorySnippetMaxRunes)
	}
	if !strings.Contains(bounded[0].Snippet, "boundedtoken") || bounded[0].Memory.Text != longText {
		t.Fatalf("bounded search result = %#v", bounded[0])
	}
}

func TestMemoryRepositoryRebuildRestoresAuthoritativeSearchResults(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	first, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "rebuildtoken first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "rebuildtoken second"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "rebuildtoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, before, second.ID, first.ID)

	if _, err := repository.db.Exec(`
		INSERT INTO memories_fts(memories_fts, rowid, text)
		SELECT 'delete', memory_id, text FROM memories WHERE memory_id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	drifted, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "rebuildtoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, drifted, second.ID)
	assertExternalContentFTSIntegrity(t, repository.db, "memories_fts", false)

	if err := repository.RebuildMemorySearchIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertExternalContentFTSIntegrity(t, repository.db, "memories_fts", true)
	after, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "rebuildtoken"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("results after rebuild = %#v, want %#v", after, before)
	}
}

func TestMemorySearchIndexIsIndependentFromPelletSearchIndex(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	memory, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "independent memory index"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec("DROP TABLE pellets_fts"); err != nil {
		t.Fatal(err)
	}
	if err := repository.RebuildMemorySearchIndex(context.Background()); err != nil {
		t.Fatalf("memory rebuild depended on pellet FTS: %v", err)
	}
	results, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "independent"})
	if err != nil {
		t.Fatalf("memory search depended on pellet FTS: %v", err)
	}
	assertMemoryIDs(t, results, memory.ID)
}

func TestMemoryRepositoryRemovalSynchronizesFTSAndNeverReusesCommittedID(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	removed, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "removetoken target"})
	if err != nil {
		t.Fatal(err)
	}
	survivor, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "removetoken survivor"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreateMemory(context.Background(), fixture.other.Project, storage.NewMemory{Text: "removetoken other"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := repository.RemoveMemory(context.Background(), fixture.linked.Project, removed.ID)
	if err != nil || !reflect.DeepEqual(got, removed) {
		t.Fatalf("RemoveMemory() = (%#v, %v), want %#v", got, err, removed)
	}
	if _, err := repository.ReadMemory(context.Background(), fixture.main.Project, removed.ID); domain.PublicError(err).Code != "memory_not_found" {
		t.Fatalf("removed ReadMemory() error = %v", err)
	}
	results, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "removetoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, results, survivor.ID)
	otherResults, err := repository.SearchMemories(context.Background(), fixture.other.Project, storage.MemorySearchOptions{Query: "removetoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, otherResults, other.ID)
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM memories_fts WHERE rowid = ?", 0, removed.ID)

	if _, err := repository.RemoveMemory(context.Background(), fixture.main.Project, other.ID); domain.PublicError(err).Code != "memory_not_found" {
		t.Fatalf("cross-project RemoveMemory() error = %v", err)
	}
	created, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "allocated after removal"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID <= other.ID || created.ID == removed.ID {
		t.Fatalf("memory ID after removal = %d, prior IDs removed=%d other=%d", created.ID, removed.ID, other.ID)
	}
}

func TestMemoryRepositoryRemovalFailureRollsBackFTSDeletion(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	memory, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "rollbacktoken remains searchable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		CREATE TRIGGER reject_memory_removal
		BEFORE DELETE ON memories
		BEGIN
			SELECT RAISE(ABORT, 'forced memory removal rollback');
		END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RemoveMemory(context.Background(), fixture.main.Project, memory.ID); domain.PublicError(err).Code != "memory_storage_failed" {
		t.Fatalf("RemoveMemory() rollback error = %v, want memory_storage_failed", err)
	}
	shown, err := repository.ReadMemory(context.Background(), fixture.main.Project, memory.ID)
	if err != nil || !reflect.DeepEqual(shown, memory) {
		t.Fatalf("memory after rollback = (%#v, %v), want %#v", shown, err, memory)
	}
	results, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "rollbacktoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertMemoryIDs(t, results, memory.ID)
}

func TestMemoryRepositoryFTSUnavailabilityIsTypedAndRemovalRollsBack(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := openMemoryRepositoryFixture(t, fixture.path)
	defer repository.Close()

	memory, err := repository.CreateMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "before unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec("DROP TABLE memories_fts"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SearchMemories(context.Background(), fixture.main.Project, storage.MemorySearchOptions{Query: "before"}); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("SearchMemories() error = %v, want fts_unavailable", err)
	}
	if err := repository.RebuildMemorySearchIndex(context.Background()); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("RebuildMemorySearchIndex() error = %v, want fts_unavailable", err)
	}
	if _, err := repository.RemoveMemory(context.Background(), fixture.main.Project, memory.ID); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("RemoveMemory() error = %v, want fts_unavailable", err)
	}
	shown, err := repository.ReadMemory(context.Background(), fixture.main.Project, memory.ID)
	if err != nil || !reflect.DeepEqual(shown, memory) {
		t.Fatalf("failed FTS removal changed authoritative memory: (%#v, %v), want %#v", shown, err, memory)
	}
}

func assertMemoryIDs(t *testing.T, results []storage.MemorySearchResult, want ...int64) {
	t.Helper()
	got := make([]int64, len(results))
	for index, result := range results {
		got[index] = result.Memory.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("memory search IDs = %v, want %v", got, want)
	}
}
