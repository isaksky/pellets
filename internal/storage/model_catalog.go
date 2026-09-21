package storage

import "context"

const ModelCatalogTTLSeconds int64 = 7 * 24 * 60 * 60

type CatalogModel struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts"`
}

// ModelCatalog is a single database-wide cache, never workspace authority.
type ModelCatalog struct {
	Version    int            `json:"version"`
	Models     []CatalogModel `json:"models"`
	FetchedAt  int64          `json:"fetched_at"`
	ClaimUntil int64          `json:"-"`
	RetryAfter int64          `json:"-"`
	Error      string         `json:"error,omitempty"`
}
type ModelCatalogReader interface {
	ReadModelCatalog(context.Context) (ModelCatalog, error)
}
type ModelCatalogWriter interface {
	ClaimModelCatalog(context.Context, string, int64, bool) (bool, error)
	CompleteModelCatalog(context.Context, string, int64, []CatalogModel, string) (bool, error)
}
