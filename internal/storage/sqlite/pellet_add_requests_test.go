package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestPelletAddRequestReplayConflictAndProjectScope(t *testing.T) {
	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()
	ctx := context.Background()
	key := "delivery-123"
	input := storage.NewPellet{RequestID: &key, Title: "original", Description: "details"}
	first, err := repository.CreatePellet(ctx, fixture.main, input)
	if err != nil {
		t.Fatal(err)
	}
	// A retry returns the creation snapshot after subsequent edits, without
	// undoing those edits, and works from another workspace of the same project.
	title := "edited"
	if _, err := repository.UpdatePellet(ctx, fixture.main, first.Reference, storage.PelletChanges{Title: &title}); err != nil {
		t.Fatal(err)
	}
	replay, err := repository.CreatePellet(ctx, fixture.linked, input)
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay=%#v first=%#v error=%v", replay, first, err)
	}
	current, err := repository.ReadPellet(ctx, fixture.main, first.Reference)
	if err != nil || current.Title != title {
		t.Fatalf("current=%#v error=%v", current, err)
	}
	input.Description = "changed"
	_, err = repository.CreatePellet(ctx, fixture.main, input)
	assertDomainErrorCode(t, err, "request_id_conflict")
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets", 1)
	// The same opaque ID is independent in another logical project.
	other, err := repository.CreatePellet(ctx, fixture.other, input)
	if err != nil || other.Reference.Number != 1 {
		t.Fatalf("other=%#v error=%v", other, err)
	}
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests", 2)
}

func TestEveryAddExpiresOldRequestsWithoutDeletingPellets(t *testing.T) {
	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()
	ctx := context.Background()
	oldKey, freshKey := "old", "fresh"
	input := storage.NewPellet{RequestID: &oldKey, Title: "keep this pellet"}
	first, err := repository.CreatePellet(ctx, fixture.main, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePellet(ctx, fixture.other, storage.NewPellet{RequestID: &freshKey, Title: "fresh"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, repository.db, "UPDATE pellet_add_requests SET created_at = julianday('now') - 3 WHERE request_id = 'old'")
	// An unkeyed add in another project still performs database-wide cleanup.
	if _, err := repository.CreatePellet(ctx, fixture.other, storage.NewPellet{Title: "unkeyed"}); err != nil {
		t.Fatal(err)
	}
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests WHERE request_id = 'old'", 0)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests WHERE request_id = 'fresh'", 1)
	if _, err := repository.ReadPellet(ctx, fixture.main, first.Reference); err != nil {
		t.Fatalf("deleted original pellet: %v", err)
	}
	repeated, err := repository.CreatePellet(ctx, fixture.main, input)
	if err != nil || repeated.Reference.Number == first.Reference.Number {
		t.Fatalf("expired request not treated as new: %#v %v", repeated, err)
	}
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets", 4)
}

func TestRequestExpiryUsesStrictOriginalTimestampAndRollsBackOnFailure(t *testing.T) {
	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()
	ctx := context.Background()
	for i, age := range []float64{2.001, 2, 1.999} {
		key := fmt.Sprintf("key-%d", i)
		if _, err := repository.CreatePellet(ctx, fixture.main, storage.NewPellet{RequestID: &key, Title: key}); err != nil {
			t.Fatal(err)
		}
		// Use a future test clock so intervening creates don't prune test rows.
		if _, err := repository.db.Exec("UPDATE pellet_add_requests SET created_at = ? WHERE request_id = ?", 3000000-age, key); err != nil {
			t.Fatal(err)
		}
	}
	conn, err := repository.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = prunePelletAddRequests(ctx, conn, 3000000)
	conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests WHERE request_id='key-0'", 0)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests", 2)
	mustExec(t, repository.db, "UPDATE pellet_add_requests SET created_at = julianday('now') - 1 WHERE request_id='key-1'")
	var before float64
	if err := repository.db.QueryRow("SELECT created_at FROM pellet_add_requests WHERE request_id='key-1'").Scan(&before); err != nil {
		t.Fatal(err)
	}
	key := "key-1"
	if _, err := repository.CreatePellet(ctx, fixture.main, storage.NewPellet{RequestID: &key, Title: key}); err != nil {
		t.Fatal(err)
	}
	var after float64
	if err := repository.db.QueryRow("SELECT created_at FROM pellet_add_requests WHERE request_id='key-1'").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("retry extended expiration")
	}
	mustExec(t, repository.db, "UPDATE pellet_add_requests SET created_at = julianday('now') - 3 WHERE request_id='key-2'")
	_, err = repository.CreatePellet(ctx, fixture.main, storage.NewPellet{Title: "invalid placement", Placement: &storage.PelletPlacement{Target: domain.PelletReference{ProjectCode: fixture.main.Project.Code, Number: 999}}})
	assertDomainErrorCode(t, err, "pellet_not_found")
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests WHERE request_id='key-2'", 1)
}

func TestConcurrentAddRequestAllocatesOnce(t *testing.T) {
	fixture := newPelletRepositoryFixture(t)
	key := "same-request"
	var wg sync.WaitGroup
	results := make(chan storage.Pellet, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		repository := fixture.open(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer repository.Close()
			pellet, err := repository.CreatePellet(context.Background(), fixture.main, storage.NewPellet{RequestID: &key, Title: "one"})
			if err != nil {
				failures <- err
				return
			}
			results <- pellet
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	for pellet := range results {
		if pellet.Reference.Number != 1 {
			t.Fatalf("duplicate allocation: %#v", pellet)
		}
	}
	repository := fixture.open(t)
	defer repository.Close()
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets", 1)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests", 1)
	assertQueryInt(t, repository.db, "SELECT next_pellet_number FROM projects WHERE code='code'", 2)
}

func TestRequestMigrationUpgradesVersionFourWithoutLosingPellets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	database, err := openWithMigrations(context.Background(), path, migrations[:4])
	if err != nil {
		t.Fatal(err)
	}
	mustInsertProjectAndWorkspace(t, database, 7, 17, "legacy", "repo")
	mustInsertPellet(t, database, 7, 23, "open", nil, intPtr(1024), nil)
	mustExec(t, database, `INSERT INTO pellets_fts(rowid,title,description,external_id) SELECT rowid,title,description,external_id FROM pellets`)
	database.Close()
	database = openTestDatabase(t, path)
	defer database.Close()
	assertPragmaInt(t, database, "user_version", LatestSchemaVersion)
	assertQueryInt(t, database, "SELECT COUNT(*) FROM pellets WHERE project_id=7 AND number=23", 1)
	assertQueryInt(t, database, "SELECT COUNT(*) FROM pellet_add_requests", 0)
}

func TestFailedRequestReceiptRollsBackPelletAllocationAndCleanup(t *testing.T) {
	fixture := newPelletRepositoryFixture(t)
	repository := fixture.open(t)
	defer repository.Close()
	ctx := context.Background()
	key := "old"
	if _, err := repository.CreatePellet(ctx, fixture.main, storage.NewPellet{RequestID: &key, Title: "existing"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, repository.db, "UPDATE pellet_add_requests SET created_at=julianday('now')-3")
	mustExec(t, repository.db, `CREATE TEMP TRIGGER fail_receipt BEFORE INSERT ON pellet_add_requests BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`)
	key = "new"
	if _, err := repository.CreatePellet(ctx, fixture.main, storage.NewPellet{RequestID: &key, Title: "must roll back"}); err == nil {
		t.Fatal("expected injected failure")
	}
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets", 1)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellets_fts", 1)
	assertQueryInt(t, repository.db, "SELECT next_pellet_number FROM projects WHERE code='code'", 2)
	assertQueryInt(t, repository.db, "SELECT COUNT(*) FROM pellet_add_requests WHERE request_id='old'", 1)
}
