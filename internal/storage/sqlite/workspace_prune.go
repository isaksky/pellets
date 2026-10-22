package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"pellets/internal/storage"
)

func (db *ProjectDatabase) PruneWorkspace(ctx context.Context, w storage.Workspace, dryRun bool) (string, error) {
	return pruneWorkspace(ctx, db.db, w, dryRun)
}
func (db *WebWriter) PruneWorkspace(ctx context.Context, w storage.Workspace, dryRun bool) (string, error) {
	return pruneWorkspace(ctx, db.db, w, dryRun)
}

func pruneWorkspace(ctx context.Context, db *sql.DB, expected storage.Workspace, dryRun bool) (string, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	w, err := findWorkspaceRow(ctx, conn, "workspace_id = ? AND pruned = 0", expected.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return "already_pruned", nil
	}
	if err != nil {
		return "", err
	}
	if w.ProjectID != expected.ProjectID || w.RootPath != expected.RootPath || w.GitDir != expected.GitDir || !w.UpdatedAt.Equal(expected.UpdatedAt) {
		return "workspace_changed", nil
	}
	var protected bool
	// A stopped attempt with released ownership is historical, as in the UI.
	// The caller separately retains cleanup receipts and live execution locks.
	err = conn.QueryRowContext(ctx, `
        SELECT EXISTS(SELECT 1 FROM pellets WHERE workspace_id = ?)
            OR EXISTS(SELECT 1 FROM execution_runs WHERE workspace_id = ?
                AND (state IN ('running','awaiting_input') OR pending_operation <> ''))`,
		w.ID, w.ID).Scan(&protected)
	if err != nil {
		return "", err
	}
	if protected {
		return "work_or_recovery_pending", nil
	}
	if !dryRun {
		if _, err = conn.ExecContext(ctx, "UPDATE project_workspaces SET pruned=1 WHERE workspace_id=?", w.ID); err != nil {
			return "", err
		}
		// Stale routing configuration must not resume if the checkout returns.
		if _, err = conn.ExecContext(ctx, "DELETE FROM workspace_group_assignments WHERE workspace_id=?", w.ID); err != nil {
			return "", err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return "", err
}
