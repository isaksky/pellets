package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

func TestPelletRepositoryRelativeAddsCoverHeadMiddleAndTail(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	first, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "second"})
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "head", Placement: &storage.PelletPlacement{Target: first.Reference, Before: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	middle, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "middle", Placement: &storage.PelletPlacement{Target: first.Reference},
	})
	if err != nil {
		t.Fatal(err)
	}
	tail, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "tail", Placement: &storage.PelletPlacement{Target: second.Reference},
	})
	if err != nil {
		t.Fatal(err)
	}

	active, err := repository.ListPellets(context.Background(), fixture.main, storage.PelletListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, active, head.Reference, first.Reference, middle.Reference, second.Reference, tail.Reference)
	assertActivePriorityInvariants(t, repository.db, fixture.main.Project.ID, 5)
}

func TestPelletRepositoryMovesInBothDirectionsAndExcludesMovingPellet(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	created := make([]storage.Pellet, 4)
	for index := range created {
		pellet, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: fmt.Sprintf("pellet %d", index+1)})
		if err != nil {
			t.Fatal(err)
		}
		created[index] = pellet
	}
	transitionPellet(t, repository, fixture.main, created[3].Reference, storage.PelletStart, nil)

	unrelatedUpdatedAt := make(map[int64]string)
	for _, pellet := range created[:3] {
		unrelatedUpdatedAt[pellet.Reference.Number] = queryPelletText(
			t, repository.db, "SELECT quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?",
			fixture.main.Project.ID, pellet.Reference.Number,
		)
	}

	moved, err := repository.MovePellet(context.Background(), fixture.linked, created[3].Reference, storage.PelletPlacement{
		Target: created[0].Reference, Before: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Status != domain.PelletInProgress || moved.Workspace == nil || moved.Workspace.ID != fixture.main.Workspace.ID {
		t.Fatalf("moving in-progress pellet changed lifecycle state: %#v", moved)
	}
	assertActiveOrder(t, repository, fixture.main, created[3].Reference, created[0].Reference, created[1].Reference, created[2].Reference)
	for number, before := range unrelatedUpdatedAt {
		if after := queryPelletText(t, repository.db, "SELECT quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, number); after != before {
			t.Fatalf("head move changed unrelated pellet %d updated_at from %s to %s", number, before, after)
		}
	}

	_, err = repository.MovePellet(context.Background(), fixture.main, created[3].Reference, storage.PelletPlacement{Target: created[1].Reference})
	if err != nil {
		t.Fatal(err)
	}
	assertActiveOrder(t, repository, fixture.main, created[0].Reference, created[1].Reference, created[3].Reference, created[2].Reference)

	_, err = repository.MovePellet(context.Background(), fixture.main, created[0].Reference, storage.PelletPlacement{Target: created[2].Reference})
	if err != nil {
		t.Fatal(err)
	}
	assertActiveOrder(t, repository, fixture.main, created[1].Reference, created[3].Reference, created[2].Reference, created[0].Reference)

	_, err = repository.MovePellet(context.Background(), fixture.main, created[2].Reference, storage.PelletPlacement{Target: created[3].Reference, Before: true})
	if err != nil {
		t.Fatal(err)
	}
	assertActiveOrder(t, repository, fixture.main, created[1].Reference, created[2].Reference, created[3].Reference, created[0].Reference)
	assertActivePriorityInvariants(t, repository.db, fixture.main.Project.ID, 4)
}

func TestPelletRepositoryRejectsInvalidMoveAndPlacementParticipants(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()

	active, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "active"})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "closed"})
	if err != nil {
		t.Fatal(err)
	}
	closed = transitionPellet(t, repository, fixture.main, closed.Reference, storage.PelletClose, nil).Pellet
	deferred, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "deferred", Status: domain.PelletMaybeLater})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "other project"})
	if err != nil {
		t.Fatal(err)
	}
	before := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID)

	for _, target := range []storage.Pellet{closed, deferred} {
		_, err := repository.MovePellet(context.Background(), fixture.main, active.Reference, storage.PelletPlacement{Target: target.Reference, Before: true})
		assertPelletErrorCode(t, err, "invalid_placement_target")
		_, err = repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
			Title: "rejected relative add", Placement: &storage.PelletPlacement{Target: target.Reference},
		})
		assertPelletErrorCode(t, err, "invalid_placement_target")
	}
	for _, source := range []storage.Pellet{closed, deferred} {
		_, err := repository.MovePellet(context.Background(), fixture.main, source.Reference, storage.PelletPlacement{Target: active.Reference})
		assertPelletErrorCode(t, err, "invalid_move_source")
	}
	_, err = repository.MovePellet(context.Background(), fixture.main, active.Reference, storage.PelletPlacement{Target: active.Reference})
	assertPelletErrorCode(t, err, "invalid_move_target")
	_, err = repository.MovePellet(context.Background(), fixture.main, active.Reference, storage.PelletPlacement{Target: other.Reference})
	assertPelletErrorCode(t, err, "reference_project_mismatch")
	_, err = repository.MovePellet(context.Background(), fixture.main, other.Reference, storage.PelletPlacement{Target: active.Reference})
	assertPelletErrorCode(t, err, "reference_project_mismatch")
	_, err = repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{
		Title: "cross-project relative add", Placement: &storage.PelletPlacement{Target: other.Reference},
	})
	assertPelletErrorCode(t, err, "reference_project_mismatch")

	if after := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected moves/placements changed project state:\nbefore=%v\nafter=%v", before, after)
	}
	if closed.Priority != nil || deferred.Priority != nil {
		t.Fatalf("inactive priorities = closed %v deferred %v, want NULL", closed.Priority, deferred.Priority)
	}
}

func TestPelletRepositoryRebalancePreservesRowsAndRollsBackWithMove(t *testing.T) {
	t.Parallel()

	t.Run("successful fresh-band rebalance", func(t *testing.T) {
		fixture := newPelletRepositoryFixture(t)
		repository := fixture.open(t)
		defer repository.Close()
		active := createTestPellets(t, repository, fixture.main, 3)
		closed := transitionPellet(t, repository, fixture.main, active[2].Reference, storage.PelletClose, nil).Pellet
		active = active[:2]
		third, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "third active"})
		if err != nil {
			t.Fatal(err)
		}
		active = append(active, third)
		deferred, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "deferred", Status: domain.PelletMaybeLater})
		if err != nil {
			t.Fatal(err)
		}
		other, err := repository.CreatePellet(context.Background(), fixture.other, storage.NewPellet{Title: "unrelated project"})
		if err != nil {
			t.Fatal(err)
		}
		setTestActivePriorities(t, repository.db, fixture.main.Project.ID, active, 1, 2, 3)
		otherBefore := captureRepositoryPelletState(t, repository.db, fixture.other.Project.ID)
		inactiveBefore := []string{
			queryPelletText(t, repository.db, "SELECT quote(priority) || '|' || quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, closed.Reference.Number),
			queryPelletText(t, repository.db, "SELECT quote(priority) || '|' || quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, deferred.Reference.Number),
		}
		firstUpdatedAt := queryPelletText(t, repository.db, "SELECT quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[0].Reference.Number)
		secondUpdatedAt := queryPelletText(t, repository.db, "SELECT quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[1].Reference.Number)

		_, err = repository.MovePellet(context.Background(), fixture.main, active[2].Reference, storage.PelletPlacement{Target: active[0].Reference})
		if err != nil {
			t.Fatal(err)
		}
		assertActiveOrder(t, repository, fixture.main, active[0].Reference, active[2].Reference, active[1].Reference)
		priorities := []int64{
			queryPelletInt64(t, repository.db, "SELECT priority FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[0].Reference.Number),
			queryPelletInt64(t, repository.db, "SELECT priority FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[2].Reference.Number),
			queryPelletInt64(t, repository.db, "SELECT priority FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[1].Reference.Number),
		}
		if !reflect.DeepEqual(priorities, []int64{1024, 1536, 2048}) {
			t.Fatalf("rebalance/move priorities = %v, want [1024 1536 2048]", priorities)
		}
		if got := queryPelletText(t, repository.db, "SELECT quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[0].Reference.Number); got != firstUpdatedAt {
			t.Fatalf("rebalance changed first unrelated updated_at from %s to %s", firstUpdatedAt, got)
		}
		if got := queryPelletText(t, repository.db, "SELECT quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, active[1].Reference.Number); got != secondUpdatedAt {
			t.Fatalf("rebalance changed second unrelated updated_at from %s to %s", secondUpdatedAt, got)
		}
		inactiveAfter := []string{
			queryPelletText(t, repository.db, "SELECT quote(priority) || '|' || quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, closed.Reference.Number),
			queryPelletText(t, repository.db, "SELECT quote(priority) || '|' || quote(updated_at) FROM pellets WHERE project_id = ? AND number = ?", fixture.main.Project.ID, deferred.Reference.Number),
		}
		if !reflect.DeepEqual(inactiveAfter, inactiveBefore) {
			t.Fatalf("rebalance touched inactive rows: before=%v after=%v", inactiveBefore, inactiveAfter)
		}
		if otherAfter := captureRepositoryPelletState(t, repository.db, fixture.other.Project.ID); !reflect.DeepEqual(otherAfter, otherBefore) {
			t.Fatalf("rebalance touched unrelated project %s: before=%v after=%v", other.Reference, otherBefore, otherAfter)
		}
		assertActivePriorityInvariants(t, repository.db, fixture.main.Project.ID, 3)
	})

	t.Run("move failure rolls back rebalance", func(t *testing.T) {
		fixture := newPelletRepositoryFixture(t)
		repository := fixture.open(t)
		defer repository.Close()
		active := createTestPellets(t, repository, fixture.main, 3)
		setTestActivePriorities(t, repository.db, fixture.main.Project.ID, active, 1, 2, 3)
		before := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID)
		if _, err := repository.db.Exec(`
			CREATE TRIGGER test_reject_final_move
			BEFORE UPDATE OF updated_at ON pellets
			BEGIN
				SELECT RAISE(ABORT, 'forced final move failure');
			END`); err != nil {
			t.Fatal(err)
		}
		_, err := repository.MovePellet(context.Background(), fixture.main, active[2].Reference, storage.PelletPlacement{Target: active[0].Reference})
		assertPelletErrorCode(t, err, "pellet_storage_failed")
		if after := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, before) {
			t.Fatalf("failed move did not roll back rebalance:\nbefore=%v\nafter=%v", before, after)
		}
	})
}

func TestPelletRepositoryRebalanceOverflowIsPreflightedBeforeUpdate(t *testing.T) {
	t.Parallel()

	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()
	active := createTestPellets(t, repository, fixture.main, 3)
	setTestActivePriorities(t, repository.db, fixture.main.Project.ID, active, math.MaxInt64-2, math.MaxInt64-1, math.MaxInt64)
	before := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID)
	changesBefore := queryTotalChanges(t, repository.db)

	_, err := repository.MovePellet(context.Background(), fixture.main, active[2].Reference, storage.PelletPlacement{Target: active[0].Reference})
	assertPelletErrorCode(t, err, "priority_conflict")
	if changesAfter := queryTotalChanges(t, repository.db); changesAfter != changesBefore {
		t.Fatalf("overflow preflight attempted an update: total_changes %d -> %d", changesBefore, changesAfter)
	}
	if after := captureRepositoryPelletState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("overflow preflight changed state:\nbefore=%v\nafter=%v", before, after)
	}
}

func TestPriorityArithmeticAndRebalanceStatementAreIntegerOnly(t *testing.T) {
	t.Parallel()

	priority, available, err := midpointPriority(math.MaxInt64-2, math.MaxInt64)
	if err != nil || !available || priority != math.MaxInt64-1 {
		t.Fatalf("large midpoint = (%d, %v, %v), want (%d, true, nil)", priority, available, err, int64(math.MaxInt64-1))
	}
	upper := strings.ToUpper(rebalanceActivePelletsSQL)
	if strings.Count(upper, "UPDATE PELLETS") != 1 || strings.Count(upper, "AS MATERIALIZED") != 2 || !strings.Contains(upper, "MAX(PRIORITY) / ?") || strings.Contains(upper, "1.0") {
		t.Fatalf("rebalance is not the single integer materialized-CTE update:\n%s", rebalanceActivePelletsSQL)
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

func TestPelletRepositoryRejectsRecoveryForOwnerlessTransitionsWithoutWrites(t *testing.T) {
	t.Parallel()

	for _, operation := range []storage.PelletLifecycleOperation{storage.PelletClose, storage.PelletDefer} {
		operation := operation
		t.Run(string(operation), func(t *testing.T) {
			fixture := newPelletRepositoryFixture(t)
			repository := fixture.open(t)
			defer repository.Close()

			pellet, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{Title: "ownerless recovery"})
			if err != nil {
				t.Fatal(err)
			}
			registeredWorkspaceID := fixture.linked.Workspace.ID
			nonexistentWorkspaceID := registeredWorkspaceID + 1000
			for _, workspaceID := range []int64{registeredWorkspaceID, nonexistentWorkspaceID} {
				before := captureRepositoryLifecycleWriteState(t, repository.db, fixture.main.Project.ID)
				changesBefore := queryTotalChanges(t, repository.db)
				_, err = repository.TransitionPellet(context.Background(), fixture.main, pellet.Reference, storage.PelletLifecycleRequest{
					Operation: operation, RecoveryWorkspaceID: &workspaceID,
				})
				assertPelletErrorCode(t, err, "recovery_workspace_mismatch")
				public := domain.PublicError(err)
				if public.Kind != domain.Conflict || public.Details["pellet_id"] != pellet.Reference.String() || public.Details["owner_workspace_id"] != nil || public.Details["provided_workspace_id"] != workspaceID {
					t.Fatalf("ownerless recovery error = %#v", public)
				}
				if changesAfter := queryTotalChanges(t, repository.db); changesAfter != changesBefore {
					t.Fatalf("rejected %s recovery attempted a SQLite write: total_changes %d -> %d", operation, changesBefore, changesAfter)
				}
				if after := captureRepositoryLifecycleWriteState(t, repository.db, fixture.main.Project.ID); !reflect.DeepEqual(after, before) {
					t.Fatalf("rejected %s recovery changed authoritative or derived state:\nbefore=%#v\nafter=%#v", operation, before, after)
				}
			}

			result := transitionPellet(t, repository, fixture.main, pellet.Reference, operation, nil)
			if operation == storage.PelletClose && result.Pellet.Status != domain.PelletClosed {
				t.Fatalf("normal ownerless close status = %q", result.Pellet.Status)
			}
			if operation == storage.PelletDefer && result.Pellet.Status != domain.PelletMaybeLater {
				t.Fatalf("normal ownerless defer status = %q", result.Pellet.Status)
			}
			if result.Pellet.Workspace != nil || result.RecoveredWorkspace != nil {
				t.Fatalf("normal ownerless %s result = %#v", operation, result)
			}

			idempotent := transitionPellet(t, repository, fixture.linked, pellet.Reference, operation, &registeredWorkspaceID)
			if !reflect.DeepEqual(idempotent.Pellet, result.Pellet) || idempotent.RecoveredWorkspace != nil {
				t.Fatalf("idempotent %s with recovery tuple changed result: first=%#v repeat=%#v", operation, result, idempotent)
			}
		})
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

type repositoryLifecycleWriteState struct {
	project    string
	workspaces []string
	pellets    []string
	pelletFTS  []string
}

func captureRepositoryLifecycleWriteState(t *testing.T, database *sql.DB, projectID int64) repositoryLifecycleWriteState {
	t.Helper()
	return repositoryLifecycleWriteState{
		project: queryPelletText(t, database, `
			SELECT quote(project_id) || '|' || quote(code) || '|' || quote(git_common_dir) || '|' ||
			       quote(git_common_dir_relative) || '|' || quote(next_pellet_number) || '|' ||
			       quote(created_at) || '|' || quote(updated_at)
			FROM projects WHERE project_id = ?`, projectID),
		workspaces: captureRepositoryRows(t, database, `
			SELECT quote(workspace_id) || '|' || quote(project_id) || '|' || quote(root_path) || '|' ||
			       quote(root_path_relative) || '|' || quote(git_dir) || '|' || quote(git_dir_relative) || '|' ||
			       quote(created_at) || '|' || quote(updated_at)
			FROM project_workspaces WHERE project_id = ? ORDER BY workspace_id`, projectID),
		pellets: captureRepositoryRows(t, database, `
			SELECT quote(rowid) || '|' || quote(project_id) || '|' || quote(workspace_id) || '|' ||
			       quote(number) || '|' || quote(title) || '|' || quote(description) || '|' ||
			       quote(external_id) || '|' || quote(group_id) || '|' || quote(status) || '|' ||
			       quote(priority) || '|' || quote(created_at) || '|' || quote(updated_at) || '|' || quote(completed_at)
			FROM pellets WHERE project_id = ? ORDER BY rowid`, projectID),
		pelletFTS: captureRepositoryRows(t, database, `
			SELECT quote(f.rowid) || '|' || quote(f.title) || '|' || quote(f.description) || '|' || quote(f.external_id)
			FROM pellets_fts AS f
			JOIN pellets AS p ON p.rowid = f.rowid
			WHERE p.project_id = ? ORDER BY f.rowid`, projectID),
	}
}

func captureRepositoryRows(t *testing.T, database *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := database.Query(query, args...)
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

func assertActiveOrder(t *testing.T, repository *PelletRepository, project storage.ResolvedProject, references ...domain.PelletReference) {
	t.Helper()
	pellets, err := repository.ListPellets(context.Background(), project, storage.PelletListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertPelletReferences(t, pellets, references...)
}

func assertActivePriorityInvariants(t *testing.T, database *sql.DB, projectID int64, want int) {
	t.Helper()
	var rows, nonNull, distinct, positive int
	if err := database.QueryRow(`
		SELECT COUNT(*), COUNT(priority), COUNT(DISTINCT priority),
		       COALESCE(SUM(CASE WHEN priority > 0 THEN 1 ELSE 0 END), 0)
		FROM pellets
		WHERE project_id = ? AND status IN ('open', 'in_progress')`, projectID).Scan(&rows, &nonNull, &distinct, &positive); err != nil {
		t.Fatal(err)
	}
	if rows != want || nonNull != want || distinct != want || positive != want {
		t.Fatalf("active priority invariants = rows %d non-null %d distinct %d positive %d, want %d each", rows, nonNull, distinct, positive, want)
	}
	assertPelletQueryInt(t, database, `
		SELECT COUNT(*) FROM pellets
		WHERE project_id = ? AND status IN ('closed', 'maybe_later') AND priority IS NOT NULL`, 0, projectID)
}

func createTestPellets(t *testing.T, repository *PelletRepository, project storage.ResolvedProject, count int) []storage.Pellet {
	t.Helper()
	pellets := make([]storage.Pellet, count)
	for index := range count {
		pellet, err := repository.CreatePellet(context.Background(), project, storage.NewPellet{Title: fmt.Sprintf("test pellet %d", index+1)})
		if err != nil {
			t.Fatal(err)
		}
		pellets[index] = pellet
	}
	return pellets
}

func setTestActivePriorities(t *testing.T, database *sql.DB, projectID int64, pellets []storage.Pellet, priorities ...int64) {
	t.Helper()
	if len(pellets) != len(priorities) {
		t.Fatalf("setTestActivePriorities received %d pellets and %d priorities", len(pellets), len(priorities))
	}
	for index, pellet := range pellets {
		result, err := database.Exec(`
			UPDATE pellets SET priority = ?
			WHERE project_id = ? AND number = ? AND priority IS NOT NULL`,
			priorities[index], projectID, pellet.Reference.Number)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			t.Fatalf("set priority for %s changed %d rows: %v", pellet.Reference, changed, err)
		}
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

func queryPelletText(t *testing.T, database *sql.DB, query string, args ...any) string {
	t.Helper()
	var value string
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
