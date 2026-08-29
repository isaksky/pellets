package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestEscapeFTS5QueryQuotesEveryOrdinaryTerm(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "pl-im9 parser_code", want: `"pl-im9" "parser_code"`},
		{input: "alpha OR (beta", want: `"alpha" "OR" "(beta"`},
		{input: `"broken`, want: `"""broken"`},
		{input: "  c++\tgithub:acme/tool#84  ", want: `"c++" "github:acme/tool#84"`},
	} {
		if got := escapeFTS5Query(test.input); got != test.want {
			t.Errorf("escapeFTS5Query(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestPelletRepositorySearchSafelyEscapesTextAndPreservesTokenCharacters(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	externalID := "github:acme/tool#84"
	indexed, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title:       `Implement pl-im9 parser_code`,
		Description: `Handle c++ (broken "quote and literal OR NOT operators`,
		ExternalID:  &externalID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "nonexistent only"}); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		"pl-im9",
		"parser_code",
		"c++",
		"(broken",
		`"quote`,
		"OR",
		"NOT",
		"github:acme/tool#84",
	} {
		results, searchErr := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: query})
		if searchErr != nil {
			t.Fatalf("SearchPellets(%q) error = %v", query, searchErr)
		}
		assertPelletReferences(t, results, indexed.Reference)
	}

	for _, query := range []string{"(", "-", `"`, `pl-im9 OR nonexistent`, `alpha) OR (*`} {
		results, searchErr := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: query})
		if searchErr != nil {
			t.Fatalf("SearchPellets(%q) treated ordinary text as syntax: %v", query, searchErr)
		}
		if len(results) != 0 {
			t.Fatalf("SearchPellets(%q) = %v, want no literal-term match", query, pelletReferences(results))
		}
	}
}

func TestPelletRepositorySearchIsProjectScopedAndUsesExactRelationalFilters(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	exactExternal, exactGroup := "Case:Exact", "Rollout/A"
	want, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "filtertoken primary", ExternalID: &exactExternal, Group: &exactGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongExternal, wrongGroup := "case:exact", "rollout/a"
	for _, input := range []storage.NewPellet{
		{Title: "filtertoken wrong external", ExternalID: &wrongExternal, Group: &exactGroup},
		{Title: "filtertoken wrong group", ExternalID: &exactExternal, Group: &wrongGroup},
	} {
		if _, err := repository.CreatePellet(context.Background(), fixture.main, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{
		Title: "filtertoken other project", ExternalID: &exactExternal, Group: &exactGroup,
	}); err != nil {
		t.Fatal(err)
	}

	results, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{
		Query: "filtertoken", ExternalID: &exactExternal, Group: &exactGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results, want.Reference)

	results, err = repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{
		Query: "Case:Exact", ExternalID: &exactExternal, Group: &exactGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results, want.Reference)

	results, err = repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{
		Query: "filtertoken", ExternalID: &wrongExternal, Group: &exactGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "filtertoken wrong external" {
		t.Fatalf("case-sensitive external-ID filter results = %#v", results)
	}
}

func TestPelletRepositorySearchIncludesEveryStatusAndRanksDeterministically(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	pellets := make([]storage.Pellet, 4)
	for index := range pellets {
		created, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "ranktoken"})
		if err != nil {
			t.Fatal(err)
		}
		pellets[index] = created
	}
	if _, err := repository.db.Exec(`
		UPDATE pellets
		SET status = 'maybe_later', priority = NULL,
		    updated_at = julianday('2030-01-01T00:00:00Z')
		WHERE project_id = ? AND number = ?`,
		fixture.main.Project.ID, pellets[2].Reference.Number,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE pellets
		SET status = 'closed', priority = NULL,
		    updated_at = julianday('2030-01-02T00:00:00Z'),
		    completed_at = julianday('2030-01-02T00:00:00Z')
		WHERE project_id = ? AND number = ?`,
		fixture.main.Project.ID, pellets[3].Reference.Number,
	); err != nil {
		t.Fatal(err)
	}

	results, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "ranktoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results,
		pellets[0].Reference,
		pellets[1].Reference,
		pellets[3].Reference,
		pellets[2].Reference,
	)

	closed := domain.PelletClosed
	limit := int64(1)
	results, err = repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{
		Query: "ranktoken", Status: &closed, Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results, pellets[3].Reference)
}

func TestPelletRepositorySearchUsesDocumentedColumnWeightsBeforeQueueTies(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	weightToken, neutral := "weighttoken", "neutral"
	externalMatch, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: neutral, Description: neutral, ExternalID: &weightToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptionMatch, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: neutral, Description: weightToken, ExternalID: &neutral,
	})
	if err != nil {
		t.Fatal(err)
	}
	titleMatch, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: weightToken, Description: neutral, ExternalID: &neutral,
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: weightToken})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results, titleMatch.Reference, descriptionMatch.Reference, externalMatch.Reference)
}

func TestPelletRepositoryRebuildRestoresAuthoritativeSearchResults(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "rebuildtoken first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "rebuildtoken second"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "rebuildtoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, before, first.Reference, second.Reference)

	if _, err := repository.db.Exec(`
		INSERT INTO pellets_fts(pellets_fts, rowid, title, description, external_id)
		SELECT 'delete', rowid, title, description, external_id
		FROM pellets WHERE project_id = ? AND number = ?`, fixture.main.Project.ID, first.Reference.Number); err != nil {
		t.Fatal(err)
	}
	drifted, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "rebuildtoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, drifted, second.Reference)

	if err := repository.RebuildPelletSearchIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "rebuildtoken"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pelletReferences(after), pelletReferences(before)) {
		t.Fatalf("results after rebuild = %v, want %v", pelletReferences(after), pelletReferences(before))
	}
}

func TestPelletRepositoryPurgeSynchronizesFTSInTheDeletionTransaction(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	oldClosed, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "purgetoken old"})
	if err != nil {
		t.Fatal(err)
	}
	newClosed, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "purgetoken new"})
	if err != nil {
		t.Fatal(err)
	}
	open, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "purgetoken open"})
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "purgetoken deferred", Status: domain.PelletMaybeLater,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherClosed, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "purgetoken other"})
	if err != nil {
		t.Fatal(err)
	}
	transitionPellet(t, repository, fixture.main, oldClosed.Reference, storage.PelletClose, nil)
	transitionPellet(t, repository, fixture.main, newClosed.Reference, storage.PelletClose, nil)
	transitionPellet(t, repository, fixture.other, otherClosed.Reference, storage.PelletClose, nil)
	if _, err := repository.db.Exec(`
		UPDATE pellets SET completed_at = julianday('2029-01-01T00:00:00Z')
		WHERE project_id = ? AND number = ?`, fixture.main.Project.ID, oldClosed.Reference.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE pellets SET completed_at = julianday('2031-01-01T00:00:00Z')
		WHERE project_id = ? AND number = ?`, fixture.main.Project.ID, newClosed.Reference.Number); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	previewed, err := repository.PreviewClosedPelletPurge(
		context.Background(), fixture.main.Project, storage.PelletPurgeOptions{CompletedBefore: &cutoff},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(previewed, []domain.PelletReference{oldClosed.Reference}) {
		t.Fatalf("previewed references = %v, want %v", previewed, []domain.PelletReference{oldClosed.Reference})
	}
	previewResults, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "purgetoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, previewResults, open.Reference, newClosed.Reference, oldClosed.Reference, deferred.Reference)
	assertPelletQueryInt(t, repository.db, "SELECT next_pellet_number FROM projects WHERE project_id = ?", 5, fixture.main.Project.ID)

	purged, err := repository.PurgeClosedPellets(context.Background(), fixture.main.Project, storage.PelletPurgeOptions{CompletedBefore: &cutoff})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(purged, []domain.PelletReference{oldClosed.Reference}) {
		t.Fatalf("purged references = %v, want %v", purged, []domain.PelletReference{oldClosed.Reference})
	}
	results, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "purgetoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results, open.Reference, newClosed.Reference, deferred.Reference)
	otherResults, err := repository.SearchPellets(context.Background(), fixture.other, storage.PelletSearchOptions{Query: "purgetoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, otherResults, otherClosed.Reference)
	assertPelletQueryInt(t, repository.db, "SELECT next_pellet_number FROM projects WHERE project_id = ?", 5, fixture.main.Project.ID)

	if _, err := repository.db.Exec(`
		CREATE TRIGGER reject_search_purge
		BEFORE DELETE ON pellets
		WHEN OLD.title = 'purgetoken new'
		BEGIN
			SELECT RAISE(ABORT, 'forced purge rollback');
		END`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PurgeClosedPellets(context.Background(), fixture.main.Project, storage.PelletPurgeOptions{}); domain.PublicError(err).Code != "pellet_storage_failed" {
		t.Fatalf("PurgeClosedPellets() rollback error = %v, want pellet_storage_failed", err)
	}
	results, err = repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "purgetoken"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, results, open.Reference, newClosed.Reference, deferred.Reference)
}

func TestPelletRepositoryFTSUnavailabilityIsTypedAndWritesRollBack(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	created, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "before unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	closed := transitionPellet(t, repository, fixture.main, created.Reference, storage.PelletClose, nil).Pellet
	if _, err := repository.db.Exec("DROP TABLE pellets_fts"); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.SearchPellets(context.Background(), fixture.main, storage.PelletSearchOptions{Query: "before"}); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("SearchPellets() error = %v, want fts_unavailable", err)
	}
	if err := repository.RebuildPelletSearchIndex(context.Background()); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("RebuildPelletSearchIndex() error = %v, want fts_unavailable", err)
	}
	if _, err := repository.PurgeClosedPellets(context.Background(), fixture.main.Project, storage.PelletPurgeOptions{}); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("PurgeClosedPellets() error = %v, want fts_unavailable", err)
	}

	updatedTitle := "edit must roll back"
	if _, err := repository.UpdatePellet(context.Background(), fixture.main, created.Reference, storage.PelletChanges{Title: &updatedTitle}); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("UpdatePellet() error = %v, want fts_unavailable", err)
	}
	read, err := repository.ReadPellet(context.Background(), fixture.main, created.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, closed) {
		t.Fatalf("failed FTS operations changed authoritative row: before %#v after %#v", closed, read)
	}

	if _, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "add must roll back"}); domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("CreatePellet() error = %v, want fts_unavailable", err)
	}
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets WHERE project_id = ?", 1, fixture.main.Project.ID)
	assertPelletQueryInt(t, repository.db, "SELECT next_pellet_number FROM projects WHERE project_id = ?", 2, fixture.main.Project.ID)
}

func pelletReferences(pellets []storage.Pellet) []domain.PelletReference {
	references := make([]domain.PelletReference, len(pellets))
	for index, pellet := range pellets {
		references[index] = pellet.Reference
	}
	return references
}
