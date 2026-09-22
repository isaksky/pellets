package app

import (
	"context"
	"errors"

	"pellets/internal/codex"
	"pellets/internal/storage"
)

// Pellet choices have final precedence for each explicitly selected field.
func pelletRunOverrides(defaults codex.RunOverrides, preferences *storage.PelletExecutionPreferences) codex.RunOverrides {
	if preferences != nil {
		if preferences.Model != nil {
			value := *preferences.Model
			defaults.Model = &value
		}
		if preferences.ReasoningEffort != nil {
			value := *preferences.ReasoningEffort
			defaults.ReasoningEffort = &value
		}
	}
	return defaults
}

func (r ExecutionRecorder) preferences(ctx context.Context, database Database, projectID, number int64) (*storage.PelletExecutionPreferences, error) {
	db, err := r.repository(ctx, database)
	if err != nil {
		return nil, err
	}
	p, err := db.ReadPelletExecutionPreferences(ctx, projectID, number)
	return p, errors.Join(err, db.Close())
}
