package sqlite

import (
	"context"
	"pellets/internal/storage"
)

func (reader *WebReader) ReadSettings(ctx context.Context) (storage.Settings, error) {
	values := storage.Settings{}
	rows, err := reader.db.QueryContext(ctx, "SELECT key,value FROM settings ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

// Initial browser preference import only fills absent keys. Explicit choices
// use last-write-wins per key; concurrent unrelated preferences cannot be lost.
func (writer *WebWriter) SaveSetting(ctx context.Context, key, value string, onlyIfUnset bool) error {
	if err := storage.ValidateSetting(key, value); err != nil {
		return err
	}
	query := "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value WHERE value<>excluded.value"
	if onlyIfUnset {
		query = "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING"
	}
	_, err := writer.db.ExecContext(ctx, query, key, value)
	return err
}
