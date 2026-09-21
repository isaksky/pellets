package app

import (
	"context"
	"errors"
	"path/filepath"
	"pellets/internal/codex"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
	"sync/atomic"
	"testing"
	"time"
)

func catalogStore(t *testing.T) (*sqlite.WebReader, *sqlite.WebWriter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	db, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	r, err := sqlite.OpenWebReader(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := sqlite.OpenWebWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	return r, w
}
func TestModelCatalogServiceTTLFailureAndForce(t *testing.T) {
	r, w := catalogStore(t)
	now := time.Unix(1000000, 0)
	calls := 0
	fail := false
	s := NewModelCatalogService(context.Background(), r, w, func(context.Context) ([]codex.ModelInfo, error) {
		calls++
		if fail {
			return nil, errors.New("private details")
		}
		return []codex.ModelInfo{{Model: "test", DisplayName: "Test", SupportedReasoningEfforts: []string{"medium"}}}, nil
	}, nil)
	s.now = func() time.Time { return now }
	defer s.cancel()
	if next := s.refresh(false); next != 7*24*time.Hour || calls != 1 {
		t.Fatal(next, calls)
	}
	now = now.Add(7*24*time.Hour - time.Second)
	if next := s.refresh(false); next != time.Second || calls != 1 {
		t.Fatal(next, calls)
	}
	now = now.Add(time.Second)
	fail = true
	if s.refresh(false) != 0 || calls != 2 {
		t.Fatal(calls)
	}
	snapshot, err := s.Snapshot(context.Background())
	if err != nil || !snapshot.Stale || snapshot.Error == "" || len(snapshot.Models) != 1 {
		t.Fatal(snapshot, err)
	}
	s.refresh(false)
	if calls != 2 {
		t.Fatal("cooldown ignored")
	}
	fail = false
	s.refresh(true)
	if calls != 3 {
		t.Fatal("manual refresh ignored")
	}
	snapshot, err = s.Snapshot(context.Background())
	if err != nil || snapshot.Stale || snapshot.Error != "" {
		t.Fatal(snapshot, err)
	}
	// A second server uses the same persisted freshness, with no discovery.
	second := NewModelCatalogService(context.Background(), r, w, func(context.Context) ([]codex.ModelInfo, error) { t.Error("fresh restart fetched"); return nil, nil }, nil)
	second.now = s.now
	defer second.cancel()
	second.refresh(false)
}
func TestModelCatalogServiceEagerDedupAndShutdown(t *testing.T) {
	r, w := catalogStore(t)
	started := make(chan struct{})
	var calls atomic.Int32
	s := NewModelCatalogService(context.Background(), r, w, func(ctx context.Context) ([]codex.ModelInfo, error) {
		calls.Add(1)
		close(started)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Minute {
			t.Error("missing refresh deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	s.Start()
	<-started
	other := NewModelCatalogService(context.Background(), r, w, func(context.Context) ([]codex.ModelInfo, error) {
		t.Error("duplicate cross-server discovery")
		return nil, nil
	}, nil)
	defer other.cancel()
	other.refresh(true)
	for range 10 {
		s.Request(true)
	}
	snapshot, err := s.Snapshot(context.Background())
	if err != nil || !snapshot.Refreshing {
		t.Fatal(snapshot, err)
	}
	s.Close()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	cached, err := r.ReadModelCatalog(context.Background())
	if err != nil || cached.ClaimUntil != 0 || cached.Error == "" {
		t.Fatal(cached, err)
	}
}
func TestCatalogSnapshotExpiredClaimRequestsRecovery(t *testing.T) {
	r, w := catalogStore(t)
	now := time.Now().Unix()
	ok, err := w.ClaimModelCatalog(context.Background(), "abandoned", now-91, true)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	s := NewModelCatalogService(context.Background(), r, w, func(context.Context) ([]codex.ModelInfo, error) { return []codex.ModelInfo{{Model: "recovered"}}, nil }, nil)
	defer s.cancel()
	s.refresh(false)
	c, _ := r.ReadModelCatalog(context.Background())
	if len(c.Models) != 1 || c.Models[0].ID != "recovered" || c.FetchedAt+storage.ModelCatalogTTLSeconds <= now {
		t.Fatal(c)
	}
}
