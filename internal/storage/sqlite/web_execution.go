package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"pellets/internal/storage"
)

// LatestWebProjectRuns reads the displayed run for every workspace in one
// snapshot. Empty workspaces cost no per-workspace query or database open.
// Use the existing decoder to retain validation, activity and recovery evidence.
func (reader *WebReader) LatestWebProjectRuns(ctx context.Context, projectID int64) (map[int64]storage.ExecutionRun, error) {
	if projectID < 1 {
		return nil, storage.InvalidExecutionRun("project ID must be positive")
	}
	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, runStorageError(err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT MAX(run_id) FROM execution_runs WHERE project_id = ? GROUP BY workspace_id`, projectID)
	if err != nil {
		return nil, runStorageError(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return nil, runStorageError(err)
	}
	runs := make(map[int64]storage.ExecutionRun, len(ids))
	for _, id := range ids {
		run, err := readExecutionRun(ctx, tx, id)
		if err != nil {
			return nil, runStorageError(err)
		}
		runs[run.WorkspaceID] = run
	}
	return runs, runStorageError(tx.Commit())
}
