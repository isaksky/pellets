package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/output"
	"pellets/internal/storage"
)

func TestPelletRepositoryAllocatesNumbersPrioritiesAndNullableOwnership(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	externalID, group := "github:example/project#1", "delivery"
	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "first open", Description: "description", ExternalID: &externalID, Group: &group,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletIdentity(t, first, "code-1", 1024)
	if first.Status != domain.PelletOpen || first.Workspace != nil || first.CompletedAt != nil {
		t.Fatalf("first pellet lifecycle fields = status %q, workspace %#v, completed %#v", first.Status, first.Workspace, first.CompletedAt)
	}

	deferred, err := repository.CreatePellet(context.Background(), fixture.linked, storage.NewPellet{
		Title: "deferred", Status: domain.PelletMaybeLater,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deferred.Reference.String() != "code-2" || deferred.Priority != nil || deferred.Workspace != nil || deferred.Status != domain.PelletMaybeLater {
		t.Fatalf("deferred pellet = %#v", deferred)
	}

	third, err := repository.CreatePellet(context.Background(), fixture.linked, storage.NewPellet{Title: "second open"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletIdentity(t, third, "code-3", 2048)
	if third.Workspace != nil {
		t.Fatalf("new open pellet unexpectedly owns workspace %#v", third.Workspace)
	}

	read, err := repository.ReadPellet(context.Background(), fixture.linked, first.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, first) {
		t.Fatalf("ReadPellet() = %#v, want %#v", read, first)
	}
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets WHERE workspace_id IS NULL", 3)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets_fts", 3)
}

func TestPelletRepositoryNumbersAreProjectLocalAndNeverReused(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "code first"})
	if err != nil {
		t.Fatal(err)
	}
	otherFirst, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "other first"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletIdentity(t, first, "code-1", 1024)
	assertPelletIdentity(t, otherFirst, "other-1", 1024)

	if _, err := repository.db.Exec(`
		INSERT INTO pellets_fts(pellets_fts, rowid, title, description, external_id)
		SELECT 'delete', rowid, title, description, external_id
		FROM pellets WHERE project_id = ? AND number = 1;
		DELETE FROM pellets WHERE project_id = ? AND number = 1`, fixture.main.Project.ID, fixture.main.Project.ID); err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "code second"})
	if err != nil {
		t.Fatal(err)
	}
	otherSecond, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "other second"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletIdentity(t, second, "code-2", 1024)
	assertPelletIdentity(t, otherSecond, "other-2", 2048)
}

func TestPelletRepositoryConcurrentLinkedWorktreeAddsShareSequences(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	firstRepository := fixture.open(t)
	defer firstRepository.Close()
	secondRepository := fixture.open(t)
	defer secondRepository.Close()

	const additions = 16
	results := make(chan storage.Pellet, additions)
	errors := make(chan error, additions)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range additions {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			repository := firstRepository
			project := fixture.main
			if index%2 == 1 {
				repository = secondRepository
				project = fixture.linked
			}
			pellet, err := repository.CreatePellet(context.Background(), project, storage.NewPellet{Title: fmt.Sprintf("concurrent %02d", index)})
			if err != nil {
				errors <- err
				return
			}
			results <- pellet
		}(index)
	}
	close(start)
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("concurrent CreatePellet() error = %v", err)
	}
	if t.Failed() {
		return
	}

	pellets := make([]storage.Pellet, 0, additions)
	for pellet := range results {
		pellets = append(pellets, pellet)
	}
	if len(pellets) != additions {
		t.Fatalf("created %d pellets, want %d", len(pellets), additions)
	}
	sort.Slice(pellets, func(left, right int) bool { return pellets[left].Reference.Number < pellets[right].Reference.Number })
	for index, pellet := range pellets {
		want := int64(index + 1)
		if pellet.Reference.Number != want || pellet.Reference.ProjectCode != "code" || pellet.Priority == nil || *pellet.Priority != want*domain.PelletPriorityStride {
			t.Fatalf("pellet %d = reference %s, priority %v", index, pellet.Reference, pellet.Priority)
		}
		if pellet.Workspace != nil {
			t.Fatalf("concurrently created pellet owns workspace %#v", pellet.Workspace)
		}
	}
	assertPelletQueryInt(t, firstRepository.db, "SELECT COUNT(DISTINCT number) FROM pellets WHERE project_id = ?", additions, fixture.main.Project.ID)
	assertPelletQueryInt(t, firstRepository.db, "SELECT COUNT(DISTINCT priority) FROM pellets WHERE project_id = ?", additions, fixture.main.Project.ID)
}

func TestPelletRepositoryCreationRollbackRestoresAllocatorsAndDerivedRows(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	if _, err := repository.db.Exec(`
		CREATE TRIGGER test_reject_pellet
		BEFORE INSERT ON pellets
		WHEN NEW.title = 'force rollback'
		BEGIN
			SELECT RAISE(ABORT, 'forced test rollback');
		END`); err != nil {
		t.Fatal(err)
	}
	_, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "force rollback"})
	if err == nil || domain.PublicError(err).Code != "pellet_storage_failed" {
		t.Fatalf("CreatePellet() error = %v", err)
	}
	assertPelletQueryInt(t, repository.db, "SELECT next_pellet_number FROM projects WHERE project_id = ?", 1, fixture.main.Project.ID)
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets WHERE project_id = ?", 0, fixture.main.Project.ID)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets_fts", 0)
	if _, err := repository.db.Exec("DROP TRIGGER test_reject_pellet"); err != nil {
		t.Fatal(err)
	}

	created, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "committed"})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletIdentity(t, created, "code-1", 1024)
}

func TestPelletRepositoryUpdateIsAtomicAndPreservesLifecycleFields(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	externalID, group := "old-external", "old-group"
	created, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "old searchable", Description: "old description", ExternalID: &externalID, Group: &group,
	})
	if err != nil {
		t.Fatal(err)
	}
	newTitle, newDescription, newGroup := "new searchable", "new description", "new-group"
	updated, err := repository.UpdatePellet(context.Background(), fixture.linked, created.Reference, storage.PelletChanges{
		Title: &newTitle, Description: &newDescription,
		ExternalID: storage.NullableTextChange{Set: true},
		Group:      storage.NullableTextChange{Set: true, Value: &newGroup},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != newTitle || updated.Description != newDescription || updated.ExternalID != nil || updated.Group == nil || *updated.Group != newGroup {
		t.Fatalf("updated editable fields = %#v", updated)
	}
	if updated.ProjectID != created.ProjectID || updated.Reference != created.Reference || updated.Status != created.Status || !reflect.DeepEqual(updated.Priority, created.Priority) || updated.Workspace != nil || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("update changed immutable/lifecycle fields: before %#v after %#v", created, updated)
	}
	assertQueryInt(t, repository.db, `SELECT COUNT(*) FROM pellets_fts WHERE pellets_fts MATCH 'old'`, 0)
	assertQueryInt(t, repository.db, `SELECT COUNT(*) FROM pellets_fts WHERE pellets_fts MATCH 'new'`, 1)

	if _, err := repository.db.Exec(`
		CREATE TRIGGER test_reject_pellet_update
		BEFORE UPDATE ON pellets
		WHEN NEW.title = 'rollback update'
		BEGIN
			SELECT RAISE(ABORT, 'forced update rollback');
		END`); err != nil {
		t.Fatal(err)
	}
	rollbackTitle := "rollback update"
	_, err = repository.UpdatePellet(context.Background(), fixture.main, created.Reference, storage.PelletChanges{Title: &rollbackTitle})
	if err == nil || domain.PublicError(err).Code != "pellet_storage_failed" {
		t.Fatalf("rollback UpdatePellet() error = %v", err)
	}
	afterRollback, err := repository.ReadPellet(context.Background(), fixture.main, created.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterRollback, updated) {
		t.Fatalf("pellet after rollback = %#v, want %#v", afterRollback, updated)
	}
	assertQueryInt(t, repository.db, `SELECT COUNT(*) FROM pellets_fts WHERE pellets_fts MATCH 'new'`, 1)
	assertQueryInt(t, repository.db, `SELECT COUNT(*) FROM pellets_fts WHERE pellets_fts MATCH 'rollback'`, 0)
}

func TestPelletRepositoryReadsLifecycleWorkspaceWithoutChangingProjectIdentity(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	owned, err := repository.CreatePellet(context.Background(), fixture.linked, storage.NewPellet{Title: "owned later"})
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := repository.CreatePellet(context.Background(), fixture.linked, storage.NewPellet{Title: "never owned", Status: domain.PelletMaybeLater})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE pellets
		SET status = 'in_progress', workspace_id = ?
		WHERE project_id = ? AND number = ?`, fixture.main.Workspace.ID, fixture.main.Project.ID, owned.Reference.Number); err != nil {
		t.Fatal(err)
	}

	readFromLinked, err := repository.ReadPellet(context.Background(), fixture.linked, owned.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if readFromLinked.ProjectID != fixture.linked.Project.ID || readFromLinked.Reference.ProjectCode != fixture.linked.Project.Code {
		t.Fatalf("linked read changed logical project identity: %#v", readFromLinked)
	}
	if readFromLinked.Workspace == nil || readFromLinked.Workspace.ID != fixture.main.Workspace.ID || readFromLinked.Workspace.ProjectID != fixture.main.Project.ID || readFromLinked.Workspace.RootPath != fixture.main.Workspace.RootPath || readFromLinked.Workspace.GitDir != fixture.main.Workspace.GitDir {
		t.Fatalf("workspace ownership = %#v, want main workspace %#v", readFromLinked.Workspace, fixture.main.Workspace)
	}
	readDeferred, err := repository.ReadPellet(context.Background(), fixture.main, deferred.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if readDeferred.Workspace != nil || readDeferred.Priority != nil {
		t.Fatalf("deferred round trip has ownership/order: %#v", readDeferred)
	}
}

func TestPelletRepositoryRejectsCrossProjectAndMissingReferences(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	otherReference := domain.PelletReference{ProjectCode: fixture.other.Project.Code, Number: 1}
	if _, err := repository.ReadPellet(context.Background(), fixture.main, otherReference); err == nil || domain.PublicError(err).Code != "reference_project_mismatch" || domain.PublicError(err).Kind != domain.Usage {
		t.Fatalf("cross-project ReadPellet() error = %v", err)
	}
	missing := domain.PelletReference{ProjectCode: fixture.main.Project.Code, Number: 99}
	if _, err := repository.ReadPellet(context.Background(), fixture.main, missing); err == nil || domain.PublicError(err).Code != "pellet_not_found" || domain.PublicError(err).Kind != domain.NotFound {
		t.Fatalf("missing ReadPellet() error = %v", err)
	}
}

func TestPelletRepositoryJulianTimestampRendersStableUTCJSON(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	created, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "timestamp"})
	if err != nil {
		t.Fatal(err)
	}
	var storageType, sqliteRendered string
	var julian float64
	if err := repository.db.QueryRow(`
		SELECT typeof(created_at), created_at,
		       strftime('%Y-%m-%dT%H:%M:%fZ', created_at)
		FROM pellets WHERE project_id = ? AND number = ?`, fixture.main.Project.ID, created.Reference.Number).Scan(&storageType, &julian, &sqliteRendered); err != nil {
		t.Fatal(err)
	}
	if storageType != "real" || julian < 2400000 {
		t.Fatalf("created_at storage = (%q, %v), want Julian REAL", storageType, julian)
	}
	formatted := output.FormatTimestamp(created.CreatedAt)
	sqliteTime, err := time.Parse("2006-01-02T15:04:05.000Z", sqliteRendered)
	if err != nil {
		t.Fatal(err)
	}
	if formatted != output.FormatTimestamp(sqliteTime) {
		t.Fatalf("formatted timestamp = %q, SQLite instant = %q", formatted, sqliteRendered)
	}
	parsed, err := time.Parse(time.RFC3339Nano, formatted)
	if err != nil || parsed.Location() != time.UTC {
		t.Fatalf("rendered timestamp %q parsed as (%v, %v)", formatted, parsed, err)
	}

	var rendered bytes.Buffer
	if err := (output.JSONRenderer{}).Render(&rendered, "show", struct {
		CreatedAt string `json:"created_at"`
	}{CreatedAt: formatted}); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rendered.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.CreatedAt != output.FormatTimestamp(sqliteTime) {
		t.Fatalf("JSON timestamp = %q, want instant %q; JSON: %s", envelope.Data.CreatedAt, sqliteRendered, rendered.String())
	}
}

func TestPelletRepositoryPlacementListFiltersAndStatusOrdering(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	externalA, externalB := "Case:Exact", "case:exact"
	groupA, groupB := "Rollout/A", "rollout/a"
	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "first", ExternalID: &externalA, Group: &groupA,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "second", ExternalID: &externalB, Group: &groupB,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := repository.CreatePellet(context.Background(), fixture.linked, storage.NewPellet{
		Title: "before first", ExternalID: &externalA, Group: &groupA,
		Placement: &storage.PelletPlacement{Target: first.Reference, Before: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := repository.CreatePellet(context.Background(), fixture.linked, storage.NewPellet{
		Title: "after first", ExternalID: &externalA, Group: &groupA,
		Placement: &storage.PelletPlacement{Target: first.Reference},
	})
	if err != nil {
		t.Fatal(err)
	}
	deferredOlder, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "deferred older", Status: domain.PelletMaybeLater, ExternalID: &externalA, Group: &groupA,
	})
	if err != nil {
		t.Fatal(err)
	}
	deferredNewer, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "deferred newer", Status: domain.PelletMaybeLater, ExternalID: &externalA, Group: &groupA,
	})
	if err != nil {
		t.Fatal(err)
	}
	closedOlder, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "closed older"})
	if err != nil {
		t.Fatal(err)
	}
	closedNewer, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "closed newer"})
	if err != nil {
		t.Fatal(err)
	}

	updates := []struct {
		query string
		args  []any
	}{
		{`UPDATE pellets SET status = 'in_progress', workspace_id = ? WHERE project_id = ? AND number = ?`, []any{fixture.linked.Workspace.ID, fixture.main.Project.ID, second.Reference.Number}},
		{`UPDATE pellets SET updated_at = julianday('2030-01-01T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, deferredOlder.Reference.Number}},
		{`UPDATE pellets SET updated_at = julianday('2030-01-02T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, deferredNewer.Reference.Number}},
		{`UPDATE pellets SET status = 'closed', priority = NULL, completed_at = julianday('2030-01-03T00:00:00Z'), updated_at = julianday('2030-01-03T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, closedOlder.Reference.Number}},
		{`UPDATE pellets SET status = 'closed', priority = NULL, completed_at = julianday('2030-01-04T00:00:00Z'), updated_at = julianday('2030-01-04T00:00:00Z') WHERE project_id = ? AND number = ?`, []any{fixture.main.Project.ID, closedNewer.Reference.Number}},
	}
	for _, update := range updates {
		if _, err := repository.db.Exec(update.query, update.args...); err != nil {
			t.Fatal(err)
		}
	}

	active, err := repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, active, before.Reference, first.Reference, after.Reference, second.Reference)
	if active[3].Workspace == nil || active[3].Workspace.ID != fixture.linked.Workspace.ID {
		t.Fatalf("in-progress list row workspace = %#v", active[3].Workspace)
	}

	exact, err := repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{
		ExternalID: &externalA, Group: &groupA,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, exact, before.Reference, first.Reference, after.Reference)

	limit := int64(2)
	all, err := repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{All: true, Limit: &limit})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, all, before.Reference, first.Reference)

	all, err = repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{All: true})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(
		t, all,
		before.Reference, first.Reference, after.Reference, second.Reference,
		deferredNewer.Reference, deferredOlder.Reference,
		closedNewer.Reference, closedOlder.Reference,
	)

	closedStatus := domain.PelletClosed
	closed, err := repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{Status: &closedStatus})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, closed, closedNewer.Reference, closedOlder.Reference)

	nextNumberBefore := queryPelletInt64(t, repository.db, "SELECT next_pellet_number FROM projects WHERE project_id = ?", fixture.main.Project.ID)
	if _, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "invalid relative add", Placement: &storage.PelletPlacement{Target: deferredOlder.Reference, Before: true},
	}); err == nil || domain.PublicError(err).Code != "invalid_placement_target" {
		t.Fatalf("relative add beside deferred pellet error = %v", err)
	}
	if nextNumberAfter := queryPelletInt64(t, repository.db, "SELECT next_pellet_number FROM projects WHERE project_id = ?", fixture.main.Project.ID); nextNumberAfter != nextNumberBefore {
		t.Fatalf("failed relative add changed allocator from %d to %d", nextNumberBefore, nextNumberAfter)
	}
}

func TestPelletRepositoryReadOnlyNextIsWorkspaceScopedAndTyped(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	matchingExternal, otherExternal := "match", "other"
	matchingGroup, otherGroup := "group", "other-group"
	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "first matching", ExternalID: &matchingExternal, Group: &matchingGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "second matching", ExternalID: &matchingExternal, Group: &matchingGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownedByMain, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "main owned outside filter", ExternalID: &otherExternal, Group: &otherGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownedByLinked, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "linked owned", ExternalID: &matchingExternal, Group: &matchingGroup,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`UPDATE pellets SET status = 'in_progress', workspace_id = ? WHERE project_id = ? AND number = ?`,
		fixture.main.Workspace.ID, fixture.main.Project.ID, ownedByMain.Reference.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`UPDATE pellets SET status = 'in_progress', workspace_id = ? WHERE project_id = ? AND number = ?`,
		fixture.linked.Workspace.ID, fixture.main.Project.ID, ownedByLinked.Reference.Number); err != nil {
		t.Fatal(err)
	}

	beforeChanges := queryTotalChanges(t, repository.db)
	mainSelection, err := repository.NextPellet(context.Background(), fixture.main, &matchingExternal, &matchingGroup)
	if err != nil {
		t.Fatal(err)
	}
	if mainSelection.Reason != storage.NextResumeInProgress || mainSelection.Pellet == nil || mainSelection.Pellet.Reference != ownedByMain.Reference {
		t.Fatalf("main next = %#v", mainSelection)
	}
	if afterChanges := queryTotalChanges(t, repository.db); afterChanges != beforeChanges {
		t.Fatalf("next changed database connection total_changes from %d to %d", beforeChanges, afterChanges)
	}

	if _, err := repository.db.Exec(`UPDATE pellets SET status = 'open', workspace_id = NULL WHERE project_id = ? AND number = ?`, fixture.main.Project.ID, ownedByMain.Reference.Number); err != nil {
		t.Fatal(err)
	}
	mainSelection, err = repository.NextPellet(context.Background(), fixture.main, &matchingExternal, &matchingGroup)
	if err != nil {
		t.Fatal(err)
	}
	if mainSelection.Reason != storage.NextOpen || mainSelection.Pellet == nil || mainSelection.Pellet.Reference != first.Reference {
		t.Fatalf("main filtered next = %#v, want %s", mainSelection, first.Reference)
	}
	if mainSelection.Pellet.Reference == ownedByLinked.Reference {
		t.Fatal("next resumed another workspace's pellet")
	}

	missing := "missing"
	none, err := repository.NextPellet(context.Background(), fixture.main, &missing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if none.Reason != storage.NextNone || none.Pellet != nil {
		t.Fatalf("empty next = %#v", none)
	}

	if _, err := repository.db.Exec(`UPDATE pellets SET status = 'open', workspace_id = NULL WHERE project_id = ? AND status = 'in_progress'`, fixture.main.Project.ID); err != nil {
		t.Fatal(err)
	}
	linkedRepository := fixture.open(t)
	defer linkedRepository.Close()
	type nextResult struct {
		selection storage.NextSelection
		err       error
	}
	start := make(chan struct{})
	results := make(chan nextResult, 2)
	var calls sync.WaitGroup
	for _, call := range []struct {
		repository *PelletRepository
		project    storage.ResolvedProject
	}{
		{repository, fixture.main},
		{linkedRepository, fixture.linked},
	} {
		calls.Add(1)
		go func(call struct {
			repository *PelletRepository
			project    storage.ResolvedProject
		}) {
			defer calls.Done()
			<-start
			selection, selectionErr := call.repository.NextPellet(context.Background(), call.project, &matchingExternal, &matchingGroup)
			results <- nextResult{selection: selection, err: selectionErr}
		}(call)
	}
	close(start)
	calls.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.selection.Reason != storage.NextOpen || result.selection.Pellet == nil || result.selection.Pellet.Reference != first.Reference {
			t.Fatalf("concurrent read-only next = %#v, want shared candidate %s", result.selection, first.Reference)
		}
	}
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets WHERE project_id = ? AND workspace_id IS NOT NULL", 0, fixture.main.Project.ID)
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets WHERE project_id = ? AND status = 'open'", 4, fixture.main.Project.ID)
	_ = second
}

func TestPelletRepositoryStartEnforcesWorkspaceOwnershipAndIdempotency(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "main work"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "linked work"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "still open"})
	if err != nil {
		t.Fatal(err)
	}

	started, err := repository.TransitionPellet(context.Background(), fixture.main, first.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if err != nil {
		t.Fatal(err)
	}
	if started.Pellet.Status != domain.PelletInProgress || started.Pellet.Workspace == nil || started.Pellet.Workspace.ID != fixture.main.Workspace.ID || !reflect.DeepEqual(started.Pellet.Priority, first.Priority) {
		t.Fatalf("started pellet = %#v", started.Pellet)
	}
	repeated, err := repository.TransitionPellet(context.Background(), fixture.main, first.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repeated.Pellet, started.Pellet) {
		t.Fatalf("idempotent start changed pellet:\nfirst=%#v\nrepeat=%#v", started.Pellet, repeated.Pellet)
	}

	beforeConflict := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID)
	_, err = repository.TransitionPellet(context.Background(), fixture.main, second.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	assertPelletErrorCode(t, err, "workspace_already_in_progress")
	if after := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, beforeConflict) {
		t.Fatalf("workspace conflict changed state:\nbefore=%v\nafter=%v", beforeConflict, after)
	}
	_, err = repository.TransitionPellet(context.Background(), fixture.linked, first.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	assertPelletErrorCode(t, err, "pellet_in_progress_elsewhere")

	linkedStarted, err := repository.TransitionPellet(context.Background(), fixture.linked, second.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if err != nil {
		t.Fatal(err)
	}
	if linkedStarted.Pellet.Workspace == nil || linkedStarted.Pellet.Workspace.ID != fixture.linked.Workspace.ID {
		t.Fatalf("linked start owner = %#v", linkedStarted.Pellet.Workspace)
	}
	assertPelletQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets WHERE project_id = ? AND status = 'in_progress'", 2, fixture.main.Project.ID)

	if _, err := repository.db.Exec(`
		UPDATE pellets SET status = 'in_progress', workspace_id = ?
		WHERE project_id = ? AND number = ?`, fixture.main.Workspace.ID, fixture.main.Project.ID, third.Reference.Number); err == nil {
		t.Fatal("database allowed a second in-progress pellet for one workspace")
	}
	other, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "other project"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.db.Exec(`
		UPDATE pellets SET status = 'in_progress', workspace_id = ?
		WHERE project_id = ? AND number = ?`, fixture.main.Workspace.ID, fixture.other.Project.ID, other.Reference.Number); err == nil {
		t.Fatal("database allowed cross-project workspace ownership")
	}
}

func TestPelletRepositoryLifecycleTransitionsRecoveryAndStableRepeats(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "recover close"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "queue tail"})
	if err != nil {
		t.Fatal(err)
	}
	started := transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletStart, nil)

	beforeConflict := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID)
	_, err = repository.TransitionPellet(context.Background(), fixture.linked, first.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletClose})
	assertPelletErrorCode(t, err, "pellet_in_progress_elsewhere")
	mismatch := fixture.linked.Workspace.ID
	_, err = repository.TransitionPellet(context.Background(), fixture.linked, first.Reference, storage.PelletLifecycleRequest{
		Operation: storage.PelletClose, RecoveryWorkspaceID: &mismatch,
	})
	assertPelletErrorCode(t, err, "recovery_workspace_mismatch")
	if after := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, beforeConflict) {
		t.Fatalf("failed ownership transitions changed state:\nbefore=%v\nafter=%v", beforeConflict, after)
	}

	ownerID := fixture.main.Workspace.ID
	closed := transitionPellet(t, repository, fixture.linked, first.Reference, storage.PelletClose, &ownerID)
	if closed.Pellet.Status != domain.PelletClosed || closed.Pellet.Priority != nil || closed.Pellet.Workspace != nil || closed.Pellet.CompletedAt == nil || closed.RecoveredWorkspace == nil || closed.RecoveredWorkspace.ID != ownerID {
		t.Fatalf("recovered close = %#v", closed)
	}
	if !closed.Pellet.UpdatedAt.After(started.Pellet.UpdatedAt) && !closed.Pellet.UpdatedAt.Equal(started.Pellet.UpdatedAt) {
		t.Fatalf("closed timestamp moved backward: start=%s close=%s", started.Pellet.UpdatedAt, closed.Pellet.UpdatedAt)
	}
	repeatedClose := transitionPellet(t, repository, fixture.linked, first.Reference, storage.PelletClose, &ownerID)
	if !reflect.DeepEqual(repeatedClose.Pellet, closed.Pellet) || repeatedClose.RecoveredWorkspace != nil {
		t.Fatalf("idempotent close changed state: first=%#v repeat=%#v", closed, repeatedClose)
	}

	reopened := transitionPellet(t, repository, fixture.linked, first.Reference, storage.PelletReopen, nil)
	if reopened.Pellet.Status != domain.PelletOpen || reopened.Pellet.Workspace != nil || reopened.Pellet.CompletedAt != nil || reopened.Pellet.Priority == nil || second.Priority == nil || *reopened.Pellet.Priority != *second.Priority+domain.PelletPriorityStride {
		t.Fatalf("reopened pellet = %#v; second priority = %v", reopened.Pellet, second.Priority)
	}
	repeatedReopen := transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletReopen, nil)
	if !reflect.DeepEqual(repeatedReopen.Pellet, reopened.Pellet) {
		t.Fatalf("idempotent reopen changed timestamp or position: first=%#v repeat=%#v", reopened.Pellet, repeatedReopen.Pellet)
	}

	deferred := transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletDefer, nil)
	if deferred.Pellet.Status != domain.PelletMaybeLater || deferred.Pellet.Priority != nil || deferred.Pellet.Workspace != nil || deferred.Pellet.CompletedAt != nil {
		t.Fatalf("deferred pellet = %#v", deferred.Pellet)
	}
	repeatedDefer := transitionPellet(t, repository, fixture.linked, first.Reference, storage.PelletDefer, nil)
	if !reflect.DeepEqual(repeatedDefer.Pellet, deferred.Pellet) {
		t.Fatalf("idempotent defer changed pellet: first=%#v repeat=%#v", deferred.Pellet, repeatedDefer.Pellet)
	}

	reopened = transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletReopen, nil)
	started = transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletStart, nil)
	released := transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletRelease, nil)
	if released.Pellet.Status != domain.PelletOpen || released.Pellet.Workspace != nil || !reflect.DeepEqual(released.Pellet.Priority, started.Pellet.Priority) {
		t.Fatalf("owner release = %#v", released.Pellet)
	}
	_, err = repository.TransitionPellet(context.Background(), fixture.main, first.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletRelease})
	assertPelletErrorCode(t, err, "invalid_pellet_transition")

	started = transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletStart, nil)
	recoveredRelease := transitionPellet(t, repository, fixture.linked, first.Reference, storage.PelletRelease, &ownerID)
	if recoveredRelease.RecoveredWorkspace == nil || recoveredRelease.RecoveredWorkspace.ID != ownerID || recoveredRelease.Pellet.Status != domain.PelletOpen || !reflect.DeepEqual(recoveredRelease.Pellet.Priority, started.Pellet.Priority) {
		t.Fatalf("recovered release = %#v", recoveredRelease)
	}

	transitionPellet(t, repository, fixture.main, first.Reference, storage.PelletStart, nil)
	recoveredDefer := transitionPellet(t, repository, fixture.linked, first.Reference, storage.PelletDefer, &ownerID)
	if recoveredDefer.RecoveredWorkspace == nil || recoveredDefer.RecoveredWorkspace.ID != ownerID || recoveredDefer.Pellet.Status != domain.PelletMaybeLater || recoveredDefer.Pellet.Priority != nil || recoveredDefer.Pellet.Workspace != nil {
		t.Fatalf("recovered defer = %#v", recoveredDefer)
	}
}

func TestPelletRepositoryStartNextIsAtomicFilteredAndBounded(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	firstRepository := fixture.open(t)
	defer firstRepository.Close()
	secondRepository := fixture.open(t)
	defer secondRepository.Close()

	externalID, group := "Case:Exact", "Rollout/A"
	first, err := firstRepository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "first", ExternalID: &externalID, Group: &group})
	if err != nil {
		t.Fatal(err)
	}
	second, err := firstRepository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "second", ExternalID: &externalID, Group: &group})
	if err != nil {
		t.Fatal(err)
	}
	otherExternal := "other"
	if _, err := firstRepository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "outside filter", ExternalID: &otherExternal, Group: &group}); err != nil {
		t.Fatal(err)
	}

	type startResult struct {
		selection storage.NextSelection
		err       error
	}
	start := make(chan struct{})
	results := make(chan startResult, 2)
	var calls sync.WaitGroup
	for _, call := range []struct {
		repository *PelletRepository
		project    storage.ResolvedProject
	}{{firstRepository, fixture.main}, {secondRepository, fixture.linked}} {
		calls.Add(1)
		go func(call struct {
			repository *PelletRepository
			project    storage.ResolvedProject
		}) {
			defer calls.Done()
			<-start
			selection, selectionErr := call.repository.StartNextPellet(context.Background(), call.project, &externalID, &group)
			results <- startResult{selection: selection, err: selectionErr}
		}(call)
	}
	close(start)
	calls.Wait()
	close(results)
	claimed := make(map[domain.PelletReference]int64)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.selection.Reason != storage.NextOpen || result.selection.Pellet == nil || result.selection.Pellet.Workspace == nil {
			t.Fatalf("start-next result = %#v", result.selection)
		}
		claimed[result.selection.Pellet.Reference] = result.selection.Pellet.Workspace.ID
	}
	if len(claimed) != 2 || claimed[first.Reference] == 0 || claimed[second.Reference] == 0 || claimed[first.Reference] == claimed[second.Reference] {
		t.Fatalf("concurrent claims = %#v", claimed)
	}
	assertPelletQueryInt(t, firstRepository.db, "SELECT COUNT(DISTINCT workspace_id) FROM pellets WHERE project_id = ? AND status = 'in_progress'", 2, fixture.main.Project.ID)

	mainOwned, err := firstRepository.StartNextPellet(context.Background(), fixture.main, &otherExternal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mainOwned.Reason != storage.NextResumeInProgress || mainOwned.Pellet == nil || mainOwned.Pellet.Workspace == nil || mainOwned.Pellet.Workspace.ID != fixture.main.Workspace.ID {
		t.Fatalf("filtered resume = %#v", mainOwned)
	}
	repeated, err := firstRepository.StartNextPellet(context.Background(), fixture.main, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repeated, mainOwned) {
		t.Fatalf("start-next resume changed pellet: first=%#v repeat=%#v", mainOwned, repeated)
	}

	for reference, owner := range claimed {
		project := fixture.main
		if owner == fixture.linked.Workspace.ID {
			project = fixture.linked
		}
		transitionPellet(t, firstRepository, project, reference, storage.PelletClose, nil)
	}
	stateBeforeEmpty := captureRepositoryPelletState(t, firstRepository.db, fixture.main.Project.ID)
	missing := "missing"
	none, err := firstRepository.StartNextPellet(context.Background(), fixture.main, &missing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if none.Reason != storage.NextNone || none.Pellet != nil {
		t.Fatalf("empty start-next = %#v", none)
	}
	if after := captureRepositoryPelletState(t, firstRepository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, stateBeforeEmpty) {
		t.Fatalf("empty start-next changed state:\nbefore=%v\nafter=%v", stateBeforeEmpty, after)
	}

	if _, err := firstRepository.db.Exec(`
		CREATE TRIGGER test_reject_start_next
		BEFORE UPDATE ON pellets
		WHEN NEW.status = 'in_progress'
		BEGIN
			SELECT RAISE(ABORT, 'forced start-next retry');
		END`); err != nil {
		t.Fatal(err)
	}
	stateBeforeRetry := captureRepositoryPelletState(t, firstRepository.db, fixture.main.Project.ID)
	_, err = firstRepository.StartNextPellet(context.Background(), fixture.main, &otherExternal, nil)
	assertPelletErrorCode(t, err, "start_next_conflict")
	if after := captureRepositoryPelletState(t, firstRepository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, stateBeforeRetry) {
		t.Fatalf("retry exhaustion changed state:\nbefore=%v\nafter=%v", stateBeforeRetry, after)
	}
}

func TestPelletRepositoryStartNextBusyLeavesNoPartialWrite(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	lockerRepository := fixture.open(t)
	defer lockerRepository.Close()
	workerRepository := fixture.open(t)
	defer workerRepository.Close()
	if _, err := workerRepository.db.Exec("PRAGMA busy_timeout = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := lockerRepository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "remains open"}); err != nil {
		t.Fatal(err)
	}
	before := captureRepositoryPelletState(t, workerRepository.db, fixture.main.Project.ID)

	connection, err := lockerRepository.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer connection.ExecContext(context.Background(), "ROLLBACK")

	_, err = workerRepository.StartNextPellet(context.Background(), fixture.linked, nil, nil)
	assertPelletErrorCode(t, err, "database_busy")
	if after := captureRepositoryPelletState(t, workerRepository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("busy start-next changed state:\nbefore=%v\nafter=%v", before, after)
	}
}

func transitionPellet(t *testing.T, repository *PelletRepository, project storage.ResolvedProject, reference domain.PelletReference, operation storage.PelletLifecycleOperation, recoveryWorkspaceID *int64) storage.PelletLifecycleResult {
	t.Helper()
	result, err := repository.TransitionPellet(context.Background(), project, reference, storage.PelletLifecycleRequest{
		Operation: operation, RecoveryWorkspaceID: recoveryWorkspaceID,
	})
	if err != nil {
		t.Fatalf("%s %s: %v", operation, reference, err)
	}
	return result
}

func assertPelletErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || domain.PublicError(err).Code != code {
		t.Fatalf("error = %v, want code %q", err, code)
	}
}

func captureRepositoryPelletState(t *testing.T, database *sql.DB, projectID int64) []string {
	t.Helper()
	rows, err := database.Query(`
		SELECT quote(number) || '|' || quote(workspace_id) || '|' || quote(status) || '|' ||
		       quote(priority) || '|' || quote(updated_at) || '|' || quote(completed_at)
		FROM pellets
		WHERE project_id = ?
		ORDER BY number`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	state := make([]string, 0)
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
	return state
}

func assertPelletReferences(t *testing.T, pellets []storage.Pellet, references ...domain.PelletReference) {
	t.Helper()
	got := make([]domain.PelletReference, len(pellets))
	for index, pellet := range pellets {
		got[index] = pellet.Reference
	}
	if !reflect.DeepEqual(got, references) {
		t.Fatalf("pellet references = %v, want %v", got, references)
	}
}

func queryTotalChanges(t *testing.T, database *sql.DB) int64 {
	t.Helper()
	var changes int64
	if err := database.QueryRow("SELECT total_changes()").Scan(&changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func queryPelletInt64(t *testing.T, database *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := database.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertPelletIdentity(t *testing.T, pellet storage.Pellet, reference string, priority int64) {
	t.Helper()
	if pellet.Reference.String() != reference || pellet.Priority == nil || *pellet.Priority != priority {
		t.Fatalf("pellet identity/order = %s, %v; want %s, %d", pellet.Reference, pellet.Priority, reference, priority)
	}
}

func assertPelletQueryInt(t *testing.T, database projectQuery, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(context.Background(), query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query result = %d, want %d; query: %s", got, want, query)
	}
}

type pelletRepositoryFixture struct {
	path                string
	main, linked, other storage.ResolvedProject
}

func newPelletRepositoryFixture(t *testing.T) pelletRepositoryFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pellets.db")
	database, err := OpenProjectDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	mainRegistration := projectRegistration("code", "repository/.git", "repository", "repository/.git")
	project, _, err := database.RegisterProject(context.Background(), mainRegistration)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	mainWorkspace := project.Workspaces[0]
	linkedRegistration := projectRegistration("code", "repository/.git", "linked", "repository/.git/worktrees/linked")
	project, _, err = database.RegisterProject(context.Background(), linkedRegistration)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	var linkedWorkspace storage.Workspace
	for _, workspace := range project.Workspaces {
		if workspace.GitDir == linkedRegistration.GitDir {
			linkedWorkspace = workspace
		}
	}
	otherRegistration := projectRegistration("other", "other/.git", "other", "other/.git")
	otherProject, _, err := database.RegisterProject(context.Background(), otherRegistration)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return pelletRepositoryFixture{
		path:   path,
		main:   storage.ResolvedProject{Project: project, Workspace: mainWorkspace},
		linked: storage.ResolvedProject{Project: project, Workspace: linkedWorkspace},
		other:  storage.ResolvedProject{Project: otherProject, Workspace: otherProject.Workspaces[0]},
	}
}

func (fixture pelletRepositoryFixture) open(t *testing.T) *PelletRepository {
	t.Helper()
	repository, err := OpenPelletRepository(context.Background(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}
