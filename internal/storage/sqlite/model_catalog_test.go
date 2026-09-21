package sqlite

import (
	"context"
	"path/filepath"
	"pellets/internal/storage"
	"sync"
	"sync/atomic"
	"testing"
)

func TestModelCatalogPersistenceClaimsAndFailure(t *testing.T) {
	f, r, w := planningFixture(t)
	ctx := context.Background()
	now := int64(1000000)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := w.ClaimModelCatalog(ctx, "first", now, false)
			if err != nil {
				t.Error(err)
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("claims=%d", winners.Load())
	}
	models := []storage.CatalogModel{{ID: "test", Name: "Test", Efforts: []string{"high"}}}
	ok, err := w.CompleteModelCatalog(ctx, "wrong", now+1, models, "")
	if err != nil || ok {
		t.Fatalf("wrong token: %v %v", ok, err)
	}
	ok, err = w.CompleteModelCatalog(ctx, "first", now+1, models, "")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	reopened, err := OpenWebReader(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ReadModelCatalog(ctx)
	if err != nil || got.FetchedAt != now+1 || len(got.Models) != 1 {
		t.Fatal(got, err)
	}
	for _, delta := range []int64{0, storage.ModelCatalogTTLSeconds - 1} {
		ok, err = w.ClaimModelCatalog(ctx, "early", now+1+delta, false)
		if err != nil || ok {
			t.Fatal("fresh claim", ok, err)
		}
	}
	expired := now + 1 + storage.ModelCatalogTTLSeconds
	ok, err = w.ClaimModelCatalog(ctx, "expired", expired, false)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	ok, err = w.CompleteModelCatalog(ctx, "expired", expired+1, nil, "SECRET raw runtime error")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, err = r.ReadModelCatalog(ctx)
	if err != nil || got.FetchedAt != now+1 || len(got.Models) != 1 || got.Error == "SECRET raw runtime error" {
		t.Fatal(got, err)
	}
	ok, _ = w.ClaimModelCatalog(ctx, "cooldown", expired+2, false)
	if ok {
		t.Fatal("ignored cooldown")
	}
	ok, _ = w.ClaimModelCatalog(ctx, "forced", expired+2, true)
	if !ok {
		t.Fatal("manual retry blocked")
	}
	ok, _ = w.ClaimModelCatalog(ctx, "recovery", expired+92, true)
	if !ok {
		t.Fatal("expired claim not recovered")
	}
	ok, _ = w.CompleteModelCatalog(ctx, "forced", expired+93, models, "")
	if ok {
		t.Fatal("superseded claim committed")
	}
	ok, _ = w.CompleteModelCatalog(ctx, "recovery", expired+93, []storage.CatalogModel{}, "")
	if !ok {
		t.Fatal("recovery failed")
	}
	got, _ = r.ReadModelCatalog(ctx)
	if len(got.Models) != 0 || got.Error != "" || got.RetryAfter != 0 {
		t.Fatal(got)
	}
}

func TestModelCatalogMigrationPreservesSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := openWithMigrations(ctx, path, migrations[:23])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO settings(key,value) VALUES('theme','icy')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var theme string
	if err = db.QueryRow(`SELECT value FROM settings WHERE key='theme'`).Scan(&theme); err != nil || theme != "icy" {
		t.Fatal(theme, err)
	}
	var version int
	if err = db.QueryRow(`SELECT version FROM model_catalog WHERE id=1`).Scan(&version); err != nil || version != 1 {
		t.Fatal(version, err)
	}
}
