package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"pellets/internal/codex"
	"pellets/internal/storage"
	"sync"
	"time"
)

type CatalogSnapshot struct {
	Models     []storage.CatalogModel `json:"models"`
	FetchedAt  int64                  `json:"fetched_at"`
	ExpiresAt  int64                  `json:"expires_at"`
	Stale      bool                   `json:"stale"`
	Refreshing bool                   `json:"refreshing"`
	Error      string                 `json:"error,omitempty"`
}
type ModelCatalogService struct {
	reader storage.ModelCatalogReader
	writer storage.ModelCatalogWriter
	fetch  func(context.Context) ([]codex.ModelInfo, error)
	now    func() time.Time
	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	force  bool
	log    io.Writer
}

func NewModelCatalogService(ctx context.Context, r storage.ModelCatalogReader, w storage.ModelCatalogWriter, fetch func(context.Context) ([]codex.ModelInfo, error), log io.Writer) *ModelCatalogService {
	ctx, cancel := context.WithCancel(ctx)
	if fetch == nil {
		fetch = codex.GlobalModels
	}
	if log == nil {
		log = io.Discard
	}
	return &ModelCatalogService{reader: r, writer: w, fetch: fetch, now: time.Now, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), log: log}
}
func (s *ModelCatalogService) Start() { go s.run() }
func (s *ModelCatalogService) Close() { s.cancel(); <-s.done }
func (s *ModelCatalogService) Request(force bool) {
	s.mu.Lock()
	s.force = s.force || force
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *ModelCatalogService) Snapshot(ctx context.Context) (CatalogSnapshot, error) {
	started := time.Now()
	c, err := s.reader.ReadModelCatalog(ctx)
	result := CatalogSnapshot{Models: c.Models, FetchedAt: c.FetchedAt, ExpiresAt: c.FetchedAt + storage.ModelCatalogTTLSeconds, Stale: c.FetchedAt == 0 || s.now().Unix() >= c.FetchedAt+storage.ModelCatalogTTLSeconds, Refreshing: c.ClaimUntil > s.now().Unix(), Error: c.Error}
	if result.Models == nil {
		result.Models = []storage.CatalogModel{}
	}
	if result.Stale && !result.Refreshing && c.RetryAfter <= s.now().Unix() && err == nil {
		s.Request(false)
	}
	if time.Since(started) > 100*time.Millisecond {
		fmt.Fprintf(s.log, "model catalog cache read: %s\n", time.Since(started))
	}
	return result, err
}
func (s *ModelCatalogService) run() {
	defer close(s.done)
	var timer *time.Timer
	var tick <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	// The first iteration eagerly checks the persistent cache.
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		force := s.force
		s.force = false
		s.mu.Unlock()
		next := s.refresh(force)
		if timer != nil {
			timer.Stop()
			timer = nil
		}
		tick = nil
		if next > 0 {
			timer = time.NewTimer(next)
			tick = timer.C
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
		case <-tick:
		}
	}
}
func (s *ModelCatalogService) refresh(force bool) time.Duration {
	c, err := s.reader.ReadModelCatalog(s.ctx)
	if err != nil {
		return 0
	}
	now := s.now().Unix()
	if c.ClaimUntil > now {
		return time.Duration(c.ClaimUntil-now) * time.Second
	}
	if !force && c.FetchedAt > 0 && c.FetchedAt+storage.ModelCatalogTTLSeconds > now {
		return time.Duration(c.FetchedAt+storage.ModelCatalogTTLSeconds-now) * time.Second
	}
	if !force && c.RetryAfter > now {
		return 0
	}
	var bytes [16]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return 0
	}
	token := hex.EncodeToString(bytes[:])
	claimed, err := s.writer.ClaimModelCatalog(s.ctx, token, now, force)
	if err != nil {
		return 0
	}
	if !claimed {
		return time.Second
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(s.ctx, 60*time.Second)
	models, err := s.fetch(ctx)
	cancel()
	message := ""
	if err != nil {
		message = "Model refresh failed. Cached choices remain available. Try Refresh models."
	}
	entries := make([]storage.CatalogModel, 0, len(models))
	for _, m := range models {
		entries = append(entries, storage.CatalogModel{ID: m.Model, Name: m.DisplayName, Efforts: m.SupportedReasoningEfforts})
	}
	// Cleanup/claim release must also complete during server shutdown.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	saved, saveErr := s.writer.CompleteModelCatalog(finishCtx, token, s.now().Unix(), entries, message)
	fmt.Fprintf(s.log, "model catalog refresh: duration=%s success=%t\n", time.Since(started), err == nil && saveErr == nil && saved)
	// Absorb duplicate refresh requests made during this attempt.
	s.mu.Lock()
	s.force = false
	s.mu.Unlock()
	select {
	case <-s.wake:
	default:
	}
	if err != nil || saveErr != nil || !saved {
		return 0
	}
	return time.Duration(storage.ModelCatalogTTLSeconds) * time.Second
}
