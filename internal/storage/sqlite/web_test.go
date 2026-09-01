package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestWebReaderIsQueryOnlyMaterializesRowsAndComposesFilters(t *testing.T) {
	t.Parallel()
	fixture := newPelletRepositoryFixture(t)
	writer, err := OpenWebWriter(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	externalID, group := "Case:Exact", "Group/One"
	first, err := writer.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{
		Title: "alpha punctuation", Description: "safe [operator] text", ExternalID: &externalID, Group: &group,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{Title: "alpha ungrouped"}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{Title: "deferred", Status: domain.PelletMaybeLater}); err != nil {
		t.Fatal(err)
	}
	memory, err := writer.CreateWebMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "web reader memory"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	observer := fixture.open(t)
	defer observer.Close()
	before := queryDataVersion(t, observer.db)
	reader, err := OpenWebReader(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	assertPragmaInt(t, reader.db, "query_only", 1)
	if _, err := reader.db.Exec("UPDATE pellets SET title = 'forbidden'"); err == nil {
		t.Fatal("query-only web reader accepted UPDATE")
	}

	projects, err := reader.ListWebProjects(context.Background())
	if err != nil || len(projects) != 2 || len(projects[0].Project.Workspaces) == 0 {
		t.Fatalf("ListWebProjects() = (%#v, %v)", projects, err)
	}
	status := domain.PelletOpen
	filtered, err := reader.ListWebPellets(context.Background(), fixture.main.Project, storage.WebPelletFilters{
		Status: &status, ExternalID: &externalID, Group: storage.WebExactFilter{Set: true, Value: &group}, Query: `alpha " OR * [`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Every malformed-looking term is safely quoted; it cannot widen results.
	if len(filtered) != 0 {
		t.Fatalf("operator-like query widened results: %#v", filtered)
	}
	filtered, err = reader.ListWebPellets(context.Background(), fixture.main.Project, storage.WebPelletFilters{
		Status: &status, ExternalID: &externalID, Group: storage.WebExactFilter{Set: true, Value: &group}, Query: "alpha",
	})
	if err != nil || len(filtered) != 1 || filtered[0].Reference != first.Reference {
		t.Fatalf("combined web filters = (%#v, %v)", filtered, err)
	}
	ungrouped, err := reader.ListWebPellets(context.Background(), fixture.main.Project, storage.WebPelletFilters{Group: storage.WebExactFilter{Set: true}})
	if err != nil || len(ungrouped) != 2 {
		t.Fatalf("ungrouped exact filter = (%#v, %v)", ungrouped, err)
	}
	groups, err := reader.ListWebGroups(context.Background(), fixture.main.Project)
	if err != nil || len(groups) != 2 || groups[0] != nil || groups[1] == nil || *groups[1] != group {
		t.Fatalf("groups = (%#v, %v)", groups, err)
	}
	memories, err := reader.ListWebMemories(context.Background(), fixture.main.Project)
	if err != nil || len(memories) != 1 || !reflect.DeepEqual(memories[0], memory) {
		t.Fatalf("memories = (%#v, %v), want %#v", memories, err, memory)
	}

	// Returned values remain usable after every query row has been closed. A
	// separate writer can commit immediately while this result is retained.
	if filtered[0].Title != "alpha punctuation" {
		t.Fatal("materialized pellet changed unexpectedly")
	}
	if afterReads := queryDataVersion(t, observer.db); afterReads != before {
		t.Fatalf("query-only web reads changed data_version from %d to %d", before, afterReads)
	}
	separate := fixture.open(t)
	if _, err := separate.UpdatePellet(context.Background(), fixture.main, first.Reference, storage.PelletChanges{Title: stringPointer("external commit")}); err != nil {
		separate.Close()
		t.Fatalf("separate writer was blocked by materialized web response: %v", err)
	}
	separate.Close()
	if after := queryDataVersion(t, observer.db); after == before {
		t.Fatal("observer did not see separate writer commit")
	}
}

func TestWebReaderSortsEveryTaskColumnWithEmptyValuesAndStableTies(t *testing.T) {
	t.Parallel()
	fixture := newPelletRepositoryFixture(t)
	writer, err := OpenWebWriter(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	alpha, beta, zulu := "alpha", "beta", "Zulu"
	alphaExternal, betaExternal, capitalAlphaExternal := "alpha", "Beta", "Alpha"
	inputs := []storage.NewPellet{
		{Title: "same", Group: &zulu},
		{Title: "same", ExternalID: &betaExternal},
		{Title: "alpha", Group: &alpha, ExternalID: &alphaExternal, Status: domain.PelletMaybeLater},
		{Title: "Bravo", Group: &alpha, Status: domain.PelletMaybeLater},
		{Title: "bravo", Group: &beta, ExternalID: &capitalAlphaExternal},
	}
	created := make([]storage.Pellet, 0, len(inputs))
	for _, input := range inputs {
		pellet, createErr := writer.CreateWebPellet(context.Background(), fixture.main.Project, input)
		if createErr != nil {
			writer.Close()
			t.Fatal(createErr)
		}
		created = append(created, pellet)
	}
	updates := []struct {
		query string
		args  []any
	}{
		{`UPDATE pellets SET status = 'in_progress', workspace_id = ?, updated_at = julianday('2030-01-05T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Workspace.ID, fixture.main.Project.ID, created[0].Reference.Number}},
		{`UPDATE pellets SET updated_at = julianday('2030-01-04T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, created[1].Reference.Number}},
		{`UPDATE pellets SET updated_at = julianday('2030-01-03T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, created[2].Reference.Number}},
		{`UPDATE pellets SET updated_at = julianday('2030-01-02T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, created[3].Reference.Number}},
		{`UPDATE pellets SET status = 'closed', priority = NULL, completed_at = julianday('2030-01-01T00:00:00Z'), updated_at = julianday('2030-01-01T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, created[4].Reference.Number}},
	}
	for _, update := range updates {
		if _, err := writer.db.Exec(update.query, update.args...); err != nil {
			writer.Close()
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenWebReader(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tests := []struct {
		name      string
		sort      storage.WebPelletSort
		wantOrder []int64
	}{
		{"reference ascending", storage.WebPelletSort{Column: storage.WebPelletSortReference, Direction: storage.WebPelletSortAscending}, []int64{1, 2, 3, 4, 5}},
		{"reference descending", storage.WebPelletSort{Column: storage.WebPelletSortReference, Direction: storage.WebPelletSortDescending}, []int64{5, 4, 3, 2, 1}},
		{"title ascending", storage.WebPelletSort{Column: storage.WebPelletSortTitle, Direction: storage.WebPelletSortAscending}, []int64{3, 4, 5, 1, 2}},
		{"title descending with stable tie", storage.WebPelletSort{Column: storage.WebPelletSortTitle, Direction: storage.WebPelletSortDescending}, []int64{1, 2, 5, 4, 3}},
		{"group ascending empty last", storage.WebPelletSort{Column: storage.WebPelletSortGroup, Direction: storage.WebPelletSortAscending}, []int64{3, 4, 5, 1, 2}},
		{"group descending empty last", storage.WebPelletSort{Column: storage.WebPelletSortGroup, Direction: storage.WebPelletSortDescending}, []int64{1, 5, 3, 4, 2}},
		{"status ascending", storage.WebPelletSort{Column: storage.WebPelletSortStatus, Direction: storage.WebPelletSortAscending}, []int64{1, 2, 3, 4, 5}},
		{"status descending", storage.WebPelletSort{Column: storage.WebPelletSortStatus, Direction: storage.WebPelletSortDescending}, []int64{5, 3, 4, 2, 1}},
		{"priority ascending empty last", storage.WebPelletSort{Column: storage.WebPelletSortPriority, Direction: storage.WebPelletSortAscending}, []int64{1, 2, 3, 4, 5}},
		{"priority descending empty last", storage.WebPelletSort{Column: storage.WebPelletSortPriority, Direction: storage.WebPelletSortDescending}, []int64{2, 1, 3, 4, 5}},
		{"external ID ascending empty last", storage.WebPelletSort{Column: storage.WebPelletSortExternalID, Direction: storage.WebPelletSortAscending}, []int64{5, 3, 2, 1, 4}},
		{"external ID descending empty last", storage.WebPelletSort{Column: storage.WebPelletSortExternalID, Direction: storage.WebPelletSortDescending}, []int64{2, 3, 5, 1, 4}},
		{"updated ascending", storage.WebPelletSort{Column: storage.WebPelletSortUpdated, Direction: storage.WebPelletSortAscending}, []int64{5, 4, 3, 2, 1}},
		{"updated descending", storage.WebPelletSort{Column: storage.WebPelletSortUpdated, Direction: storage.WebPelletSortDescending}, []int64{1, 2, 3, 4, 5}},
		{"invalid pair uses safe defaults", storage.WebPelletSort{Column: "unknown", Direction: "sideways"}, []int64{1, 2, 3, 4, 5}},
		{"invalid direction retains valid column", storage.WebPelletSort{Column: storage.WebPelletSortTitle, Direction: "sideways"}, []int64{3, 4, 5, 1, 2}},
		{"invalid column retains valid direction", storage.WebPelletSort{Column: "unknown", Direction: storage.WebPelletSortDescending}, []int64{2, 1, 3, 4, 5}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pellets, err := reader.ListWebPellets(context.Background(), fixture.main.Project, storage.WebPelletFilters{Sort: test.sort})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, 0, len(pellets))
			for _, pellet := range pellets {
				got = append(got, pellet.Reference.Number)
			}
			if !reflect.DeepEqual(got, test.wantOrder) {
				t.Fatalf("order = %v, want %v", got, test.wantOrder)
			}
		})
	}
}

func TestDataVersionMonitorUsesOnePinnedConnectionAndObservesBothWriterKinds(t *testing.T) {
	t.Parallel()
	fixture := newPelletRepositoryFixture(t)
	monitor, err := OpenDataVersionMonitor(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer monitor.Close()
	if got := monitor.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("monitor MaxOpenConnections = %d, want 1", got)
	}
	baseline, err := monitor.DataVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	external := fixture.open(t)
	if _, err := external.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "external"}); err != nil {
		t.Fatal(err)
	}
	external.Close()
	afterExternal, err := monitor.DataVersion(context.Background())
	if err != nil || afterExternal == baseline {
		t.Fatalf("external version = (%d, %v), baseline %d", afterExternal, err, baseline)
	}

	webWriter, err := OpenWebWriter(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := webWriter.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{Title: "web"}); err != nil {
		t.Fatal(err)
	}
	afterWeb, err := monitor.DataVersion(context.Background())
	if err != nil || afterWeb == afterExternal {
		t.Fatalf("web version = (%d, %v), previous %d", afterWeb, err, afterExternal)
	}
	webWriter.Close()

	raw, err := sql.Open(driverName, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE pellets SET title = 'rolled back'"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	afterRollback, err := monitor.DataVersion(context.Background())
	if err != nil || afterRollback != afterWeb {
		t.Fatalf("rollback changed data_version from %d to %d (%v)", afterWeb, afterRollback, err)
	}
}

func TestWebOptimisticWritesConflictAndMemoryFTSIsAtomic(t *testing.T) {
	t.Parallel()
	fixture := newPelletRepositoryFixture(t)
	writer, err := OpenWebWriter(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	pellet, err := writer.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{Title: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	version := storage.PelletVersion(pellet)

	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, title := range []string{"first contender", "second contender"} {
		group.Add(1)
		go func(title string) {
			defer group.Done()
			<-start
			_, err := writer.UpdateWebPellet(context.Background(), fixture.main.Project, pellet.Reference, version, storage.PelletChanges{Title: &title})
			results <- err
		}(title)
	}
	close(start)
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var conflict *storage.OptimisticConflict
		if errors.As(err, &conflict) && conflict.Pellet != nil {
			conflicts++
			continue
		}
		t.Fatalf("unexpected optimistic update error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("optimistic outcomes = %d success, %d conflict", successes, conflicts)
	}

	externalPellet, err := writer.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{Title: "before CLI edit"})
	if err != nil {
		t.Fatal(err)
	}
	externalVersion := storage.PelletVersion(externalPellet)
	external := fixture.open(t)
	cliTitle := "committed by CLI connection"
	if _, err := external.UpdatePellet(context.Background(), fixture.main, externalPellet.Reference, storage.PelletChanges{Title: &cliTitle}); err != nil {
		external.Close()
		t.Fatal(err)
	}
	external.Close()
	webTitle := "stale browser overwrite"
	_, err = writer.UpdateWebPellet(context.Background(), fixture.main.Project, externalPellet.Reference, externalVersion, storage.PelletChanges{Title: &webTitle})
	var externalConflict *storage.OptimisticConflict
	if !errors.As(err, &externalConflict) || externalConflict.Pellet == nil || externalConflict.Pellet.Title != cliTitle {
		t.Fatalf("external CLI/web conflict = %v, %#v", err, externalConflict)
	}

	memory, err := writer.CreateWebMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "old searchable text", CreatedBy: domain.MemoryCreatedByAgent})
	if err != nil || memory.ApprovedAt != nil {
		t.Fatalf("created memory = (%#v, %v)", memory, err)
	}
	memory, err = writer.ApproveWebMemory(context.Background(), fixture.main.Project, memory.ID, storage.MemoryVersion(memory))
	if err != nil || memory.ApprovedAt == nil {
		t.Fatalf("approved memory = (%#v, %v)", memory, err)
	}
	approvedVersion := storage.MemoryVersion(memory)
	updated, err := writer.UpdateWebMemory(context.Background(), fixture.main.Project, memory.ID, approvedVersion, "new searchable text")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ApprovedAt != nil {
		t.Fatal("editing reviewed memory retained approval")
	}
	assertPelletQueryInt(t, writer.db, `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH '"old"'`, 0)
	assertPelletQueryInt(t, writer.db, `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH '"new"'`, 1)
	if _, err := writer.UpdateWebMemory(context.Background(), fixture.main.Project, memory.ID, approvedVersion, "stale overwrite"); err == nil {
		t.Fatal("stale memory update succeeded")
	} else {
		var conflict *storage.OptimisticConflict
		if !errors.As(err, &conflict) || conflict.Memory == nil || conflict.Memory.Text != "new searchable text" {
			t.Fatalf("stale memory conflict = %v, %#v", err, conflict)
		}
	}
}

func TestWebWriterBusyAndFTSFailuresAreBoundedAndWriteFree(t *testing.T) {
	t.Parallel()
	fixture := newPelletRepositoryFixture(t)
	writer, err := OpenWebWriter(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	pellet, err := writer.CreateWebPellet(context.Background(), fixture.main.Project, storage.NewPellet{Title: "busy original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.Exec("PRAGMA busy_timeout = 60"); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open(driverName, fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	if _, err := locker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = locker.Exec("ROLLBACK")
		}
	}()
	changed := "must not commit"
	started := time.Now()
	_, err = writer.UpdateWebPellet(context.Background(), fixture.main.Project, pellet.Reference, storage.PelletVersion(pellet), storage.PelletChanges{Title: &changed})
	elapsed := time.Since(started)
	if err == nil || domain.PublicError(err).Code != "database_busy" {
		t.Fatalf("busy web update error = %v", err)
	}
	if elapsed < 40*time.Millisecond || elapsed > time.Second {
		t.Fatalf("busy web update elapsed %s, want bounded wait", elapsed)
	}
	if _, err := locker.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	locked = false
	unchanged, err := writer.pelletRepository().ReadPellet(context.Background(), fixture.main, pellet.Reference)
	if err != nil || unchanged.Title != pellet.Title {
		t.Fatalf("busy update changed row: %#v, %v", unchanged, err)
	}

	memory, err := writer.CreateWebMemory(context.Background(), fixture.main.Project, storage.NewMemory{Text: "atomic old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.Exec("DROP TABLE memories_fts"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.UpdateWebMemory(context.Background(), fixture.main.Project, memory.ID, storage.MemoryVersion(memory), "atomic new"); err == nil || domain.PublicError(err).Code != "fts_unavailable" {
		t.Fatalf("FTS failure update error = %v", err)
	}
	var text string
	if err := writer.db.QueryRow("SELECT text FROM memories WHERE memory_id = ?", memory.ID).Scan(&text); err != nil || text != "atomic old" {
		t.Fatalf("FTS failure authoritative text = %q, %v", text, err)
	}
}

func queryDataVersion(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var version int64
	if err := db.QueryRow("PRAGMA data_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func stringPointer(value string) *string { return &value }
