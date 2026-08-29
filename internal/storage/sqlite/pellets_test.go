package sqlite

import (
	"bytes"
	"context"
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
