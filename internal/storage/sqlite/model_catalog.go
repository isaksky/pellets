package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"pellets/internal/storage"
)

func (r *WebReader) ReadModelCatalog(ctx context.Context) (storage.ModelCatalog, error) {
	var c storage.ModelCatalog
	var data string
	err := r.db.QueryRowContext(ctx, `SELECT version,models,fetched_at,claim_until,retry_after,error FROM model_catalog WHERE id=1`).Scan(&c.Version, &data, &c.FetchedAt, &c.ClaimUntil, &c.RetryAfter, &c.Error)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(data), &c.Models)
	if c.Models == nil {
		c.Models = []storage.CatalogModel{}
	}
	return c, err
}
func (w *WebWriter) ClaimModelCatalog(ctx context.Context, token string, now int64, force bool) (bool, error) {
	if token == "" {
		return false, fmt.Errorf("empty catalog claim")
	}
	result, err := w.db.ExecContext(ctx, `UPDATE model_catalog SET claim_token=?,claim_until=?,error='' WHERE id=1 AND claim_until<=? AND (? OR ((fetched_at=0 OR fetched_at<=?) AND retry_after<=?))`, token, now+90, now, force, now-storage.ModelCatalogTTLSeconds, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (w *WebWriter) CompleteModelCatalog(ctx context.Context, token string, now int64, models []storage.CatalogModel, message string) (bool, error) {
	// Only caller-supplied fixed public messages belong in this cache.
	if message != "" {
		message = "Model refresh failed. Cached choices remain available. Try Refresh models."
	}
	data, err := json.Marshal(models)
	if err != nil {
		return false, err
	}
	result, err := w.db.ExecContext(ctx, `UPDATE model_catalog SET models=CASE WHEN ?='' THEN ? ELSE models END,fetched_at=CASE WHEN ?='' THEN ? ELSE fetched_at END,error=?,retry_after=CASE WHEN ?='' THEN 0 ELSE ? END,claim_token='',claim_until=0 WHERE id=1 AND claim_token=? AND claim_until>?`, message, string(data), message, now, message, message, now+60, token, now)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
