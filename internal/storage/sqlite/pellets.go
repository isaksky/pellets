package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

const pelletSelect = `
	SELECT p.project_id, project.code, p.number,
	       p.title, p.description, p.external_id, p.group_id,
	       p.status, p.priority,
	       strftime('%Y-%m-%dT%H:%M:%fZ', p.created_at),
	       strftime('%Y-%m-%dT%H:%M:%fZ', p.updated_at),
	       strftime('%Y-%m-%dT%H:%M:%fZ', p.completed_at),
	       workspace.workspace_id, workspace.project_id,
	       workspace.root_path, workspace.root_path_relative,
	       workspace.git_dir, workspace.git_dir_relative,
	       strftime('%Y-%m-%dT%H:%M:%fZ', workspace.created_at),
	       strftime('%Y-%m-%dT%H:%M:%fZ', workspace.updated_at)
	FROM pellets AS p
	JOIN projects AS project ON project.project_id = p.project_id
	LEFT JOIN project_workspaces AS workspace
	  ON workspace.project_id = p.project_id
	 AND workspace.workspace_id = p.workspace_id`

// PelletRepository owns a configured real SQLite database used for core
// pellet allocation and persistence.
type PelletRepository struct {
	db *sql.DB
}

func OpenPelletRepository(ctx context.Context, databasePath string) (*PelletRepository, error) {
	db, err := Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	return &PelletRepository{db: db}, nil
}

func (repository *PelletRepository) Close() error { return repository.db.Close() }

// CreatePellet allocates the project-local number and tail priority in the
// same immediate transaction as the authoritative and FTS inserts.
func (repository *PelletRepository) CreatePellet(ctx context.Context, project storage.ResolvedProject, input storage.NewPellet) (storage.Pellet, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.Pellet{}, err
	}
	normalized, err := validateNewPellet(input)
	if err != nil {
		return storage.Pellet{}, err
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("open create connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Pellet{}, pelletStorageError("begin pellet creation", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("capture pellet creation timestamp", err)
	}
	if err := ensureStoredProject(ctx, connection, project.Project); err != nil {
		return storage.Pellet{}, err
	}
	var priority *int64
	if normalized.Status == domain.PelletOpen {
		var allocated int64
		if normalized.Placement == nil {
			allocated, err = allocateTailPriority(ctx, connection, project.Project.ID)
		} else {
			allocated, err = allocatePlacedPriority(ctx, connection, project, *normalized.Placement, 0)
		}
		if err != nil {
			return storage.Pellet{}, err
		}
		priority = &allocated
	}

	var number int64
	if err := connection.QueryRowContext(ctx, `
		UPDATE projects
		SET next_pellet_number = next_pellet_number + 1,
		    updated_at = ?
		WHERE project_id = ?
		RETURNING next_pellet_number - 1`, timestamp, project.Project.ID).Scan(&number); err != nil {
		return storage.Pellet{}, pelletStorageError("allocate pellet number", err)
	}

	result, err := connection.ExecContext(ctx, `
		INSERT INTO pellets(
			project_id, workspace_id, number, title, description, external_id,
			group_id, status, priority, created_at, updated_at, completed_at
		) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		project.Project.ID, number, normalized.Title, normalized.Description,
		nullableTextValue(normalized.ExternalID), nullableTextValue(normalized.Group),
		normalized.Status, nullableInt64Value(priority), timestamp, timestamp)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("insert pellet", err)
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read inserted pellet row identity", err)
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO pellets_fts(rowid, title, description, external_id)
		VALUES (?, ?, ?, ?)`, rowID, normalized.Title, normalized.Description, nullableTextValue(normalized.ExternalID)); err != nil {
		return storage.Pellet{}, pelletFTSError("index inserted pellet", err)
	}

	pellet, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, number)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read inserted pellet", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Pellet{}, pelletStorageError("commit pellet creation", err)
	}
	committed = true
	return pellet, nil
}

// MovePellet changes one active pellet's relative position in the same
// immediate transaction as any gap-exhaustion rebalance. Neighbor selection
// excludes the moving row in both directions.
func (repository *PelletRepository) MovePellet(
	ctx context.Context,
	project storage.ResolvedProject,
	reference domain.PelletReference,
	placement storage.PelletPlacement,
) (storage.Pellet, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.Pellet{}, err
	}
	if err := validateReferenceProject(project, reference); err != nil {
		return storage.Pellet{}, err
	}
	if err := validateReferenceProject(project, placement.Target); err != nil {
		return storage.Pellet{}, err
	}
	if reference == placement.Target {
		return storage.Pellet{}, invalidMoveTarget(reference)
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("open move connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Pellet{}, pelletStorageError("begin pellet move", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := ensureStoredProject(ctx, connection, project.Project); err != nil {
		return storage.Pellet{}, err
	}
	moving, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, reference.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Pellet{}, pelletNotFound(reference)
	}
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read pellet before move", err)
	}
	if !activePelletStatus(moving.Status) || moving.Priority == nil || *moving.Priority <= 0 {
		return storage.Pellet{}, invalidMoveSource(moving)
	}

	priority, err := allocatePlacedPriority(ctx, connection, project, placement, reference.Number)
	if err != nil {
		return storage.Pellet{}, err
	}
	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("capture pellet move timestamp", err)
	}
	result, err := connection.ExecContext(ctx, `
		UPDATE pellets
		SET priority = ?, updated_at = ?
		WHERE project_id = ? AND number = ?
		  AND status IN ('open', 'in_progress') AND priority IS NOT NULL`,
		priority, timestamp, project.Project.ID, reference.Number)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("update moved pellet", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err == nil {
			err = fmt.Errorf("updated %d rows, want 1", changed)
		}
		return storage.Pellet{}, pelletStorageError("verify moved pellet update", err)
	}

	moved, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, reference.Number)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read moved pellet", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Pellet{}, pelletStorageError("commit pellet move", err)
	}
	committed = true
	return moved, nil
}

func (repository *PelletRepository) ReadPellet(ctx context.Context, project storage.ResolvedProject, reference domain.PelletReference) (storage.Pellet, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.Pellet{}, err
	}
	if err := validateReferenceProject(project, reference); err != nil {
		return storage.Pellet{}, err
	}
	pellet, err := loadPellet(ctx, repository.db, project.Project.ID, project.Project.Code, reference.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Pellet{}, pelletNotFound(reference)
	}
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read pellet", err)
	}
	return pellet, nil
}

// ListPellets returns only the resolved logical project's rows using the
// command contract's status-specific ordering.
func (repository *PelletRepository) ListPellets(ctx context.Context, project storage.ResolvedProject, options storage.PelletListOptions) ([]storage.Pellet, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return nil, err
	}
	if err := validatePelletListOptions(options); err != nil {
		return nil, err
	}

	query := pelletSelect + "\n\tWHERE p.project_id = ?"
	arguments := []any{project.Project.ID}
	if options.Status != nil {
		query += " AND p.status = ?"
		arguments = append(arguments, *options.Status)
	} else if !options.All {
		query += " AND p.status IN ('in_progress', 'open')"
	}
	if options.ExternalID != nil {
		query += " AND p.external_id = ?"
		arguments = append(arguments, *options.ExternalID)
	}
	if options.Group != nil {
		query += " AND p.group_id = ?"
		arguments = append(arguments, *options.Group)
	}
	query += pelletListOrder(options)
	if options.Limit != nil {
		query += " LIMIT ?"
		arguments = append(arguments, *options.Limit)
	}

	rows, err := repository.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, pelletStorageError("list pellets", err)
	}
	defer rows.Close()
	pellets := make([]storage.Pellet, 0)
	for rows.Next() {
		pellet, scanErr := scanPellet(rows)
		if scanErr != nil {
			return nil, pelletStorageError("read listed pellet", scanErr)
		}
		pellets = append(pellets, pellet)
	}
	if err := rows.Err(); err != nil {
		return nil, pelletStorageError("iterate listed pellets", err)
	}
	return pellets, nil
}

// SearchPellets uses only the derived FTS5 index for candidate retrieval. The
// project, external-ID, group, and status constraints remain authoritative
// relational predicates over pellets.
func (repository *PelletRepository) SearchPellets(ctx context.Context, project storage.ResolvedProject, options storage.PelletSearchOptions) ([]storage.Pellet, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return nil, err
	}
	if err := validatePelletSearchOptions(options); err != nil {
		return nil, err
	}

	query := `
		SELECT p.project_id, project.code, p.number,
		       p.title, p.description, p.external_id, p.group_id,
		       p.status, p.priority,
		       strftime('%Y-%m-%dT%H:%M:%fZ', p.created_at),
		       strftime('%Y-%m-%dT%H:%M:%fZ', p.updated_at),
		       strftime('%Y-%m-%dT%H:%M:%fZ', p.completed_at),
		       workspace.workspace_id, workspace.project_id,
		       workspace.root_path, workspace.root_path_relative,
		       workspace.git_dir, workspace.git_dir_relative,
		       strftime('%Y-%m-%dT%H:%M:%fZ', workspace.created_at),
		       strftime('%Y-%m-%dT%H:%M:%fZ', workspace.updated_at)
		FROM pellets_fts
		JOIN pellets AS p ON p.rowid = pellets_fts.rowid
		JOIN projects AS project ON project.project_id = p.project_id
		LEFT JOIN project_workspaces AS workspace
		  ON workspace.project_id = p.project_id
		 AND workspace.workspace_id = p.workspace_id
		WHERE pellets_fts MATCH ? AND p.project_id = ?`
	arguments := []any{escapeFTS5Query(options.Query), project.Project.ID}
	if options.Status != nil {
		query += " AND p.status = ?"
		arguments = append(arguments, *options.Status)
	}
	if options.ExternalID != nil {
		query += " AND p.external_id = ?"
		arguments = append(arguments, *options.ExternalID)
	}
	if options.Group != nil {
		query += " AND p.group_id = ?"
		arguments = append(arguments, *options.Group)
	}
	query += `
		ORDER BY bm25(pellets_fts, 8.0, 2.0, 1.0),
		         p.priority IS NULL,
		         p.priority,
		         p.updated_at DESC,
		         p.number`
	if options.Limit != nil {
		query += " LIMIT ?"
		arguments = append(arguments, *options.Limit)
	}

	rows, err := repository.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, pelletFTSError("search pellets", err)
	}
	defer rows.Close()
	pellets := make([]storage.Pellet, 0)
	for rows.Next() {
		pellet, scanErr := scanPellet(rows)
		if scanErr != nil {
			return nil, pelletStorageError("read pellet search result", scanErr)
		}
		pellets = append(pellets, pellet)
	}
	if err := rows.Err(); err != nil {
		return nil, pelletFTSError("iterate pellet search results", err)
	}
	return pellets, nil
}

// RebuildPelletSearchIndex regenerates the disposable external-content index
// from all authoritative pellet rows under one writer transaction.
func (repository *PelletRepository) RebuildPelletSearchIndex(ctx context.Context) error {
	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return pelletStorageError("open pellet search rebuild connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return pelletStorageError("begin pellet search rebuild", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := connection.ExecContext(ctx, "INSERT INTO pellets_fts(pellets_fts) VALUES ('rebuild')"); err != nil {
		return pelletFTSError("rebuild pellet search index", err)
	}
	if _, err := connection.ExecContext(ctx, "INSERT INTO pellets_fts(pellets_fts) VALUES ('integrity-check')"); err != nil {
		return pelletFTSError("verify rebuilt pellet search index", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return pelletStorageError("commit pellet search rebuild", err)
	}
	committed = true
	return nil
}

// PurgeClosedPellets removes the selected authoritative rows and their
// external-content FTS entries under the same immediate transaction. The
// separate purge command owns confirmation and dry-run policy.
func (repository *PelletRepository) PurgeClosedPellets(ctx context.Context, project storage.Project, options storage.PelletPurgeOptions) ([]domain.PelletReference, error) {
	if err := validatePelletProject(project); err != nil {
		return nil, err
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return nil, pelletStorageError("open pellet purge connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, pelletStorageError("begin pellet purge", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := ensureStoredProject(ctx, connection, project); err != nil {
		return nil, err
	}

	predicate := "project_id = ? AND status = 'closed'"
	arguments := []any{project.ID}
	if options.CompletedBefore != nil {
		predicate += " AND completed_at < julianday(?)"
		arguments = append(arguments, options.CompletedBefore.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
	}
	rows, err := connection.QueryContext(ctx, "SELECT number FROM pellets WHERE "+predicate+" ORDER BY number", arguments...)
	if err != nil {
		return nil, pelletStorageError("select closed pellets for purge", err)
	}
	references := make([]domain.PelletReference, 0)
	for rows.Next() {
		var number int64
		if err := rows.Scan(&number); err != nil {
			_ = rows.Close()
			return nil, pelletStorageError("read closed pellet purge selection", err)
		}
		references = append(references, domain.PelletReference{ProjectCode: project.Code, Number: number})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, pelletStorageError("iterate closed pellet purge selection", err)
	}
	if err := rows.Close(); err != nil {
		return nil, pelletStorageError("close closed pellet purge selection", err)
	}

	if _, err := connection.ExecContext(ctx, `
		INSERT INTO pellets_fts(pellets_fts, rowid, title, description, external_id)
		SELECT 'delete', rowid, title, description, external_id
		FROM pellets WHERE `+predicate, arguments...); err != nil {
		return nil, pelletFTSError("remove purged pellets from search index", err)
	}
	result, err := connection.ExecContext(ctx, "DELETE FROM pellets WHERE "+predicate, arguments...)
	if err != nil {
		return nil, pelletStorageError("delete purged pellets", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return nil, pelletStorageError("verify purged pellet count", err)
	}
	if deleted != int64(len(references)) {
		return nil, pelletStorageError("verify purged pellet count", fmt.Errorf("deleted %d rows, selected %d", deleted, len(references)))
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, pelletStorageError("commit pellet purge", err)
	}
	committed = true
	return references, nil
}

// NextPellet reads one consistent selection and never begins a write
// transaction. The current workspace's owned pellet deliberately bypasses the
// optional filters.
func (repository *PelletRepository) NextPellet(ctx context.Context, project storage.ResolvedProject, externalID, group *string) (storage.NextSelection, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.NextSelection{}, err
	}
	if err := validateNullablePelletText("external_id", externalID); err != nil {
		return storage.NextSelection{}, err
	}
	if err := validateNullablePelletText("group", group); err != nil {
		return storage.NextSelection{}, err
	}

	query := pelletSelect + `
	WHERE p.project_id = ?
	  AND (
	        (p.status = 'in_progress' AND p.workspace_id = ?)
	        OR (p.status = 'open'`
	arguments := []any{project.Project.ID, project.Workspace.ID}
	if externalID != nil {
		query += " AND p.external_id = ?"
		arguments = append(arguments, *externalID)
	}
	if group != nil {
		query += " AND p.group_id = ?"
		arguments = append(arguments, *group)
	}
	query += `)
	      )
	ORDER BY CASE WHEN p.status = 'in_progress' AND p.workspace_id = ? THEN 0 ELSE 1 END,
	         p.priority,
	         p.number
	LIMIT 1`
	arguments = append(arguments, project.Workspace.ID)

	pellet, err := scanPellet(repository.db.QueryRowContext(ctx, query, arguments...))
	if errors.Is(err, sql.ErrNoRows) {
		return storage.NextSelection{Reason: storage.NextNone}, nil
	}
	if err != nil {
		return storage.NextSelection{}, pelletStorageError("select next pellet", err)
	}
	reason := storage.NextOpen
	if pellet.Status == domain.PelletInProgress && pellet.Workspace != nil && pellet.Workspace.ID == project.Workspace.ID {
		reason = storage.NextResumeInProgress
	}
	return storage.NextSelection{Reason: reason, Pellet: &pellet}, nil
}

const startNextMaxAttempts = 2

// StartNextPellet holds selection and ownership assignment under one immediate
// transaction. The retry is deliberately bounded and deterministic even
// though BEGIN IMMEDIATE normally prevents a competing writer from changing
// the selected row before assignment.
func (repository *PelletRepository) StartNextPellet(ctx context.Context, project storage.ResolvedProject, externalID, group *string) (storage.NextSelection, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.NextSelection{}, err
	}
	if err := validateNullablePelletText("external_id", externalID); err != nil {
		return storage.NextSelection{}, err
	}
	if err := validateNullablePelletText("group", group); err != nil {
		return storage.NextSelection{}, err
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.NextSelection{}, pelletStorageError("open start-next connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.NextSelection{}, pelletStorageError("begin start-next", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := ensureStoredProjectWorkspace(ctx, connection, project); err != nil {
		return storage.NextSelection{}, err
	}
	for attempt := 0; attempt < startNextMaxAttempts; attempt++ {
		owned, err := loadWorkspaceInProgressPellet(ctx, connection, project)
		if err == nil {
			selection := storage.NextSelection{Reason: storage.NextResumeInProgress, Pellet: &owned}
			if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
				return storage.NextSelection{}, pelletStorageError("commit start-next resume", err)
			}
			committed = true
			return selection, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return storage.NextSelection{}, pelletStorageError("read current workspace pellet for start-next", err)
		}

		candidate, err := loadNextOpenPellet(ctx, connection, project, externalID, group)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
				return storage.NextSelection{}, pelletStorageError("commit empty start-next", err)
			}
			committed = true
			return storage.NextSelection{Reason: storage.NextNone}, nil
		}
		if err != nil {
			return storage.NextSelection{}, pelletStorageError("select open pellet for start-next", err)
		}

		timestamp, err := captureJulianTimestamp(ctx, connection)
		if err != nil {
			return storage.NextSelection{}, pelletStorageError("capture start-next timestamp", err)
		}
		result, err := connection.ExecContext(ctx, `
			UPDATE pellets
			SET status = 'in_progress', workspace_id = ?, updated_at = ?
			WHERE project_id = ? AND number = ?
			  AND status = 'open' AND workspace_id IS NULL`,
			project.Workspace.ID, timestamp, project.Project.ID, candidate.Reference.Number)
		if err != nil {
			if isSQLiteConstraint(err) {
				continue
			}
			return storage.NextSelection{}, pelletStorageError("assign start-next pellet", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return storage.NextSelection{}, pelletStorageError("read start-next assignment result", err)
		}
		if changed != 1 {
			continue
		}
		started, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, candidate.Reference.Number)
		if err != nil {
			return storage.NextSelection{}, pelletStorageError("read started next pellet", err)
		}
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			return storage.NextSelection{}, pelletStorageError("commit start-next assignment", err)
		}
		committed = true
		return storage.NextSelection{Reason: storage.NextOpen, Pellet: &started}, nil
	}

	return storage.NextSelection{}, domain.NewError(
		domain.Conflict,
		"start_next_conflict",
		"could not assign the next eligible pellet after bounded retry",
		map[string]any{"workspace_id": project.Workspace.ID, "attempts": startNextMaxAttempts},
	)
}

// TransitionPellet enforces the normative workspace-aware transition table in
// one immediate transaction. Idempotent target-state repeats commit without
// updating timestamps or queue positions.
func (repository *PelletRepository) TransitionPellet(ctx context.Context, project storage.ResolvedProject, reference domain.PelletReference, request storage.PelletLifecycleRequest) (storage.PelletLifecycleResult, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.PelletLifecycleResult{}, err
	}
	if err := validateReferenceProject(project, reference); err != nil {
		return storage.PelletLifecycleResult{}, err
	}
	if err := validatePelletLifecycleRequest(request); err != nil {
		return storage.PelletLifecycleResult{}, err
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.PelletLifecycleResult{}, pelletStorageError("open lifecycle connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.PelletLifecycleResult{}, pelletStorageError("begin pellet lifecycle transition", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if err := ensureStoredProjectWorkspace(ctx, connection, project); err != nil {
		return storage.PelletLifecycleResult{}, err
	}
	before, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, reference.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.PelletLifecycleResult{}, pelletNotFound(reference)
	}
	if err != nil {
		return storage.PelletLifecycleResult{}, pelletStorageError("read pellet before lifecycle transition", err)
	}

	after, recovered, changed, err := applyPelletLifecycleTransition(ctx, connection, project, before, request)
	if err != nil {
		return storage.PelletLifecycleResult{}, err
	}
	if changed {
		timestamp, err := captureJulianTimestamp(ctx, connection)
		if err != nil {
			return storage.PelletLifecycleResult{}, pelletStorageError("capture pellet lifecycle timestamp", err)
		}
		var completedAt any
		if request.Operation == storage.PelletClose {
			completedAt = timestamp
		}
		result, err := connection.ExecContext(ctx, `
			UPDATE pellets
			SET status = ?, workspace_id = ?, priority = ?, completed_at = ?, updated_at = ?
			WHERE project_id = ? AND number = ?`,
			after.Status, pelletWorkspaceID(after.Workspace), nullableInt64Value(after.Priority), completedAt, timestamp,
			project.Project.ID, reference.Number)
		if err != nil {
			return storage.PelletLifecycleResult{}, pelletStorageError("update pellet lifecycle state", err)
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			if err == nil {
				err = fmt.Errorf("updated %d rows, want 1", rows)
			}
			return storage.PelletLifecycleResult{}, pelletStorageError("verify pellet lifecycle update", err)
		}
		after, err = loadPellet(ctx, connection, project.Project.ID, project.Project.Code, reference.Number)
		if err != nil {
			return storage.PelletLifecycleResult{}, pelletStorageError("read transitioned pellet", err)
		}
	}

	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.PelletLifecycleResult{}, pelletStorageError("commit pellet lifecycle transition", err)
	}
	committed = true
	return storage.PelletLifecycleResult{Pellet: after, RecoveredWorkspace: recovered}, nil
}

func applyPelletLifecycleTransition(
	ctx context.Context,
	query projectQuery,
	project storage.ResolvedProject,
	pellet storage.Pellet,
	request storage.PelletLifecycleRequest,
) (storage.Pellet, *storage.Workspace, bool, error) {
	switch request.Operation {
	case storage.PelletStart:
		switch pellet.Status {
		case domain.PelletInProgress:
			if pellet.Workspace != nil && pellet.Workspace.ID == project.Workspace.ID {
				return pellet, nil, false, nil
			}
			return storage.Pellet{}, nil, false, pelletOwnedElsewhere(pellet)
		case domain.PelletOpen:
			owned, err := loadWorkspaceInProgressPellet(ctx, query, project)
			if err == nil {
				return storage.Pellet{}, nil, false, workspaceAlreadyInProgress(project.Workspace.ID, owned.Reference)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return storage.Pellet{}, nil, false, pelletStorageError("check workspace before start", err)
			}
			pellet.Status = domain.PelletInProgress
			workspace := project.Workspace
			pellet.Workspace = &workspace
			return pellet, nil, true, nil
		default:
			return storage.Pellet{}, nil, false, invalidPelletTransition(request.Operation, pellet)
		}

	case storage.PelletRelease:
		if pellet.Status != domain.PelletInProgress {
			return storage.Pellet{}, nil, false, invalidPelletTransition(request.Operation, pellet)
		}
		recovered, err := authorizeOwnedPelletTransition(project, pellet, request.RecoveryWorkspaceID)
		if err != nil {
			return storage.Pellet{}, nil, false, err
		}
		pellet.Status = domain.PelletOpen
		pellet.Workspace = nil
		return pellet, recovered, true, nil

	case storage.PelletClose:
		if pellet.Status == domain.PelletClosed {
			return pellet, nil, false, nil
		}
		var recovered *storage.Workspace
		if pellet.Status == domain.PelletInProgress {
			var err error
			recovered, err = authorizeOwnedPelletTransition(project, pellet, request.RecoveryWorkspaceID)
			if err != nil {
				return storage.Pellet{}, nil, false, err
			}
		} else if pellet.Status != domain.PelletOpen {
			return storage.Pellet{}, nil, false, invalidPelletTransition(request.Operation, pellet)
		} else if request.RecoveryWorkspaceID != nil {
			return storage.Pellet{}, nil, false, recoveryWorkspaceMismatch(pellet, *request.RecoveryWorkspaceID)
		}
		pellet.Status = domain.PelletClosed
		pellet.Workspace = nil
		pellet.Priority = nil
		return pellet, recovered, true, nil

	case storage.PelletReopen:
		if pellet.Status == domain.PelletOpen {
			return pellet, nil, false, nil
		}
		if pellet.Status != domain.PelletClosed && pellet.Status != domain.PelletMaybeLater {
			return storage.Pellet{}, nil, false, invalidPelletTransition(request.Operation, pellet)
		}
		priority, err := allocateTailPriority(ctx, query, project.Project.ID)
		if err != nil {
			return storage.Pellet{}, nil, false, err
		}
		pellet.Status = domain.PelletOpen
		pellet.Workspace = nil
		pellet.Priority = &priority
		pellet.CompletedAt = nil
		return pellet, nil, true, nil

	case storage.PelletDefer:
		if pellet.Status == domain.PelletMaybeLater {
			return pellet, nil, false, nil
		}
		var recovered *storage.Workspace
		if pellet.Status == domain.PelletInProgress {
			var err error
			recovered, err = authorizeOwnedPelletTransition(project, pellet, request.RecoveryWorkspaceID)
			if err != nil {
				return storage.Pellet{}, nil, false, err
			}
		} else if pellet.Status != domain.PelletOpen {
			return storage.Pellet{}, nil, false, invalidPelletTransition(request.Operation, pellet)
		} else if request.RecoveryWorkspaceID != nil {
			return storage.Pellet{}, nil, false, recoveryWorkspaceMismatch(pellet, *request.RecoveryWorkspaceID)
		}
		pellet.Status = domain.PelletMaybeLater
		pellet.Workspace = nil
		pellet.Priority = nil
		return pellet, recovered, true, nil
	}

	return storage.Pellet{}, nil, false, domain.NewError(
		domain.Usage,
		"invalid_lifecycle_operation",
		"unknown pellet lifecycle operation",
		map[string]any{"operation": request.Operation},
	)
}

func authorizeOwnedPelletTransition(project storage.ResolvedProject, pellet storage.Pellet, recoveryWorkspaceID *int64) (*storage.Workspace, error) {
	if pellet.Workspace == nil {
		return nil, domain.NewError(domain.Storage, "pellet_storage_failed", "stored in-progress pellet has no workspace owner", nil)
	}
	ownerID := pellet.Workspace.ID
	if ownerID == project.Workspace.ID && recoveryWorkspaceID == nil {
		return nil, nil
	}
	if recoveryWorkspaceID == nil {
		return nil, pelletOwnedElsewhere(pellet)
	}
	if *recoveryWorkspaceID != ownerID {
		return nil, recoveryWorkspaceMismatch(pellet, *recoveryWorkspaceID)
	}
	recovered := *pellet.Workspace
	return &recovered, nil
}

func recoveryWorkspaceMismatch(pellet storage.Pellet, providedWorkspaceID int64) error {
	var ownerWorkspaceID any
	if pellet.Workspace != nil {
		ownerWorkspaceID = pellet.Workspace.ID
	}
	return domain.NewError(
		domain.Conflict,
		"recovery_workspace_mismatch",
		"the supplied recovery workspace does not own the pellet",
		map[string]any{
			"pellet_id": pellet.Reference.String(), "owner_workspace_id": ownerWorkspaceID,
			"provided_workspace_id": providedWorkspaceID,
		},
	)
}

func loadWorkspaceInProgressPellet(ctx context.Context, query projectQuery, project storage.ResolvedProject) (storage.Pellet, error) {
	return scanPellet(query.QueryRowContext(ctx, pelletSelect+`
		WHERE p.project_id = ? AND p.status = 'in_progress' AND p.workspace_id = ?
		ORDER BY p.priority, p.number
		LIMIT 1`, project.Project.ID, project.Workspace.ID))
}

func loadNextOpenPellet(ctx context.Context, query projectQuery, project storage.ResolvedProject, externalID, group *string) (storage.Pellet, error) {
	statement := pelletSelect + `
		WHERE p.project_id = ? AND p.status = 'open'`
	arguments := []any{project.Project.ID}
	if externalID != nil {
		statement += " AND p.external_id = ?"
		arguments = append(arguments, *externalID)
	}
	if group != nil {
		statement += " AND p.group_id = ?"
		arguments = append(arguments, *group)
	}
	statement += " ORDER BY p.priority, p.number LIMIT 1"
	return scanPellet(query.QueryRowContext(ctx, statement, arguments...))
}

func ensureStoredProjectWorkspace(ctx context.Context, query projectQuery, project storage.ResolvedProject) error {
	if err := ensureStoredProject(ctx, query, project.Project); err != nil {
		return err
	}
	var workspaceID int64
	err := query.QueryRowContext(ctx, `
		SELECT workspace_id
		FROM project_workspaces
		WHERE project_id = ? AND workspace_id = ?`, project.Project.ID, project.Workspace.ID).Scan(&workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NewError(
			domain.NotFound,
			"workspace_not_registered",
			"the current Git worktree is not registered for the logical project",
			map[string]any{"project": project.Project.Code, "workspace_id": project.Workspace.ID},
		)
	}
	if err != nil {
		return pelletStorageError("verify pellet workspace", err)
	}
	return nil
}

func validatePelletLifecycleRequest(request storage.PelletLifecycleRequest) error {
	switch request.Operation {
	case storage.PelletStart, storage.PelletRelease, storage.PelletClose, storage.PelletReopen, storage.PelletDefer:
	default:
		return domain.NewError(
			domain.Usage, "invalid_lifecycle_operation", "unknown pellet lifecycle operation",
			map[string]any{"operation": request.Operation},
		)
	}
	if request.RecoveryWorkspaceID == nil {
		return nil
	}
	if request.Operation != storage.PelletRelease && request.Operation != storage.PelletClose && request.Operation != storage.PelletDefer {
		return domain.NewError(
			domain.Usage, "recovery_not_supported", "this lifecycle operation does not accept workspace recovery",
			map[string]any{"operation": request.Operation},
		)
	}
	if *request.RecoveryWorkspaceID <= 0 {
		return domain.NewError(
			domain.Usage, "invalid_workspace_id", "workspace ID must be a positive integer",
			map[string]any{"workspace_id": *request.RecoveryWorkspaceID},
		)
	}
	return nil
}

func workspaceAlreadyInProgress(workspaceID int64, reference domain.PelletReference) error {
	return domain.NewError(
		domain.Conflict,
		"workspace_already_in_progress",
		fmt.Sprintf("workspace %d already owns %s", workspaceID, reference),
		map[string]any{"workspace_id": workspaceID, "pellet_id": reference.String()},
	)
}

func pelletOwnedElsewhere(pellet storage.Pellet) error {
	ownerID := int64(0)
	if pellet.Workspace != nil {
		ownerID = pellet.Workspace.ID
	}
	return domain.NewError(
		domain.Conflict,
		"pellet_in_progress_elsewhere",
		fmt.Sprintf("%s is in progress in workspace %d", pellet.Reference, ownerID),
		map[string]any{"pellet_id": pellet.Reference.String(), "workspace_id": ownerID},
	)
}

func invalidPelletTransition(operation storage.PelletLifecycleOperation, pellet storage.Pellet) error {
	return domain.NewError(
		domain.Conflict,
		"invalid_pellet_transition",
		fmt.Sprintf("cannot %s %s from status %s", operation, pellet.Reference, pellet.Status),
		map[string]any{"command": operation, "pellet_id": pellet.Reference.String(), "status": pellet.Status},
	)
}

func pelletWorkspaceID(workspace *storage.Workspace) any {
	if workspace == nil {
		return nil
	}
	return workspace.ID
}

func isSQLiteConstraint(err error) bool {
	var sqliteError interface{ Code() int }
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == 19
}

// UpdatePellet edits only user-editable scalar fields and updates FTS in the
// same immediate transaction when indexed text changes.
func (repository *PelletRepository) UpdatePellet(ctx context.Context, project storage.ResolvedProject, reference domain.PelletReference, changes storage.PelletChanges) (storage.Pellet, error) {
	if err := validatePelletProjectContext(project); err != nil {
		return storage.Pellet{}, err
	}
	if err := validateReferenceProject(project, reference); err != nil {
		return storage.Pellet{}, err
	}
	if err := validatePelletChanges(changes); err != nil {
		return storage.Pellet{}, err
	}

	connection, err := repository.db.Conn(ctx)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("open update connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Pellet{}, pelletStorageError("begin pellet update", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	before, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, reference.Number)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Pellet{}, pelletNotFound(reference)
	}
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read pellet before update", err)
	}
	after := applyPelletChanges(before, changes)
	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("capture pellet update timestamp", err)
	}
	indexedTextChanged := before.Title != after.Title || before.Description != after.Description || !equalNullableText(before.ExternalID, after.ExternalID)
	if indexedTextChanged {
		if _, err := connection.ExecContext(ctx, `
			INSERT INTO pellets_fts(pellets_fts, rowid, title, description, external_id)
			SELECT 'delete', rowid, title, description, external_id
			FROM pellets
			WHERE project_id = ? AND number = ?`, project.Project.ID, reference.Number); err != nil {
			return storage.Pellet{}, pelletFTSError("remove old pellet search text", err)
		}
	}
	if _, err := connection.ExecContext(ctx, `
		UPDATE pellets
		SET title = ?, description = ?, external_id = ?, group_id = ?, updated_at = ?
		WHERE project_id = ? AND number = ?`,
		after.Title, after.Description, nullableTextValue(after.ExternalID), nullableTextValue(after.Group), timestamp,
		project.Project.ID, reference.Number); err != nil {
		return storage.Pellet{}, pelletStorageError("update pellet", err)
	}
	if indexedTextChanged {
		if _, err := connection.ExecContext(ctx, `
			INSERT INTO pellets_fts(rowid, title, description, external_id)
			SELECT rowid, title, description, external_id
			FROM pellets
			WHERE project_id = ? AND number = ?`, project.Project.ID, reference.Number); err != nil {
			return storage.Pellet{}, pelletFTSError("index updated pellet", err)
		}
	}
	updated, err := loadPellet(ctx, connection, project.Project.ID, project.Project.Code, reference.Number)
	if err != nil {
		return storage.Pellet{}, pelletStorageError("read updated pellet", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Pellet{}, pelletStorageError("commit pellet update", err)
	}
	committed = true
	return updated, nil
}

func captureJulianTimestamp(ctx context.Context, query projectQuery) (float64, error) {
	var timestamp float64
	err := query.QueryRowContext(ctx, "SELECT julianday('now')").Scan(&timestamp)
	return timestamp, err
}

func ensureStoredProject(ctx context.Context, query projectQuery, project storage.Project) error {
	var storedCode string
	err := query.QueryRowContext(ctx, "SELECT code FROM projects WHERE project_id = ?", project.ID).Scan(&storedCode)
	if errors.Is(err, sql.ErrNoRows) {
		return projectNotFound(map[string]any{"code": project.Code})
	}
	if err != nil {
		return pelletStorageError("verify pellet project", err)
	}
	if storedCode != project.Code {
		return domain.NewError(
			domain.Conflict,
			"project_identity_mismatch",
			"the resolved logical project identity does not match the selected database",
			map[string]any{"project_id": project.ID, "resolved_code": project.Code, "stored_code": storedCode},
		)
	}
	return nil
}

func allocateTailPriority(ctx context.Context, query projectQuery, projectID int64) (int64, error) {
	var maximum sql.NullInt64
	if err := query.QueryRowContext(ctx, "SELECT MAX(priority) FROM pellets WHERE project_id = ? AND priority IS NOT NULL", projectID).Scan(&maximum); err != nil {
		return 0, pelletStorageError("read active pellet tail", err)
	}
	if !maximum.Valid {
		return domain.PelletPriorityStride, nil
	}
	if maximum.Int64 > math.MaxInt64-domain.PelletPriorityStride {
		return 0, domain.NewError(
			domain.Conflict,
			"priority_conflict",
			"the active pellet priority range is exhausted",
			map[string]any{"project_id": projectID},
		)
	}
	return maximum.Int64 + domain.PelletPriorityStride, nil
}

func allocatePlacedPriority(ctx context.Context, query projectQuery, project storage.ResolvedProject, placement storage.PelletPlacement, excludedNumber int64) (int64, error) {
	if err := validateReferenceProject(project, placement.Target); err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		priority, available, err := placedPriority(ctx, query, project.Project.ID, placement, excludedNumber)
		if err != nil {
			return 0, err
		}
		if available {
			return priority, nil
		}
		if attempt == 0 {
			if err := rebalanceActivePellets(ctx, query, project.Project.ID); err != nil {
				return 0, err
			}
		}
	}
	return 0, domain.NewError(
		domain.Conflict,
		"priority_conflict",
		"no integer priority is available at the requested queue position",
		map[string]any{"project": project.Project.Code, "target": placement.Target.String()},
	)
}

func placedPriority(ctx context.Context, query projectQuery, projectID int64, placement storage.PelletPlacement, excludedNumber int64) (int64, bool, error) {
	var targetPriority sql.NullInt64
	var targetStatus domain.PelletStatus
	err := query.QueryRowContext(ctx, `
		SELECT priority, status
		FROM pellets
		WHERE project_id = ? AND number = ?`, projectID, placement.Target.Number).Scan(&targetPriority, &targetStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, pelletNotFound(placement.Target)
	}
	if err != nil {
		return 0, false, pelletStorageError("read placement target", err)
	}
	if targetStatus != domain.PelletOpen && targetStatus != domain.PelletInProgress {
		return 0, false, domain.NewError(
			domain.Conflict,
			"invalid_placement_target",
			"a pellet may only be positioned relative to active work",
			map[string]any{"target": placement.Target.String(), "status": targetStatus},
		)
	}
	if !targetPriority.Valid || targetPriority.Int64 <= 0 {
		return 0, false, domain.NewError(domain.Storage, "pellet_storage_failed", "stored active pellet priority is inconsistent", nil)
	}
	priority := targetPriority.Int64

	if placement.Before {
		var previous sql.NullInt64
		if err := query.QueryRowContext(ctx, `
			SELECT MAX(priority)
			FROM pellets
			WHERE project_id = ? AND priority < ? AND number <> ?`, projectID, priority, excludedNumber).Scan(&previous); err != nil {
			return 0, false, pelletStorageError("read previous placement priority", err)
		}
		if !previous.Valid {
			if priority > domain.PelletPriorityStride {
				return priority - domain.PelletPriorityStride, true, nil
			}
			return 0, false, nil
		}
		return midpointPriority(previous.Int64, priority)
	}

	var next sql.NullInt64
	if err := query.QueryRowContext(ctx, `
		SELECT MIN(priority)
		FROM pellets
		WHERE project_id = ? AND priority > ? AND number <> ?`, projectID, priority, excludedNumber).Scan(&next); err != nil {
		return 0, false, pelletStorageError("read next placement priority", err)
	}
	if !next.Valid {
		if priority <= math.MaxInt64-domain.PelletPriorityStride {
			return priority + domain.PelletPriorityStride, true, nil
		}
		return 0, false, nil
	}
	return midpointPriority(priority, next.Int64)
}

func midpointPriority(lower, upper int64) (int64, bool, error) {
	if lower <= 0 || upper <= lower {
		return 0, false, domain.NewError(domain.Storage, "pellet_storage_failed", "stored active pellet priorities are inconsistent", nil)
	}
	if upper-lower <= 1 {
		return 0, false, nil
	}
	return lower + (upper-lower)/2, true, nil
}

const rebalanceActivePelletsSQL = `
	WITH
	bounds AS MATERIALIZED (
		SELECT ((MAX(priority) / ?) + 1) * ? AS base
		FROM pellets
		WHERE project_id = ? AND priority IS NOT NULL
	),
	ranked AS MATERIALIZED (
		SELECT p.rowid,
		       b.base + (ROW_NUMBER() OVER (ORDER BY p.priority, p.number) - 1) * ? AS new_priority
		FROM pellets AS p
		CROSS JOIN bounds AS b
		WHERE p.project_id = ? AND p.priority IS NOT NULL
	)
	UPDATE pellets AS p
	SET priority = (SELECT r.new_priority FROM ranked AS r WHERE r.rowid = p.rowid)
	WHERE p.project_id = ? AND p.priority IS NOT NULL`

func rebalanceActivePellets(ctx context.Context, query projectQuery, projectID int64) error {
	var maximum sql.NullInt64
	var count int64
	if err := query.QueryRowContext(ctx, `
		SELECT MAX(priority), COUNT(*)
		FROM pellets
		WHERE project_id = ? AND priority IS NOT NULL`, projectID).Scan(&maximum, &count); err != nil {
		return pelletStorageError("preflight active pellet rebalance", err)
	}
	if !maximum.Valid || count == 0 {
		return nil
	}
	stride := int64(domain.PelletPriorityStride)
	if maximum.Int64 > math.MaxInt64-stride {
		return priorityRebalanceOverflow(projectID)
	}
	base := (maximum.Int64/stride + 1) * stride
	if count-1 > (math.MaxInt64-base)/stride {
		return priorityRebalanceOverflow(projectID)
	}
	if _, err := query.ExecContext(
		ctx, rebalanceActivePelletsSQL,
		stride, stride, projectID, stride, projectID, projectID,
	); err != nil {
		return pelletStorageError("rebalance active pellets", err)
	}
	return nil
}

func priorityRebalanceOverflow(projectID int64) error {
	return domain.NewError(
		domain.Conflict,
		"priority_conflict",
		"the active pellet priority range cannot be rebalanced safely",
		map[string]any{"project_id": projectID},
	)
}

func activePelletStatus(status domain.PelletStatus) bool {
	return status == domain.PelletOpen || status == domain.PelletInProgress
}

func invalidMoveTarget(reference domain.PelletReference) error {
	return domain.NewError(
		domain.Usage,
		"invalid_move_target",
		"a pellet cannot be moved relative to itself",
		map[string]any{"pellet_id": reference.String()},
	)
}

func invalidMoveSource(pellet storage.Pellet) error {
	return domain.NewError(
		domain.Conflict,
		"invalid_move_source",
		"only active work can be moved",
		map[string]any{"pellet_id": pellet.Reference.String(), "status": pellet.Status},
	)
}

func pelletListOrder(options storage.PelletListOptions) string {
	if options.Status != nil {
		switch *options.Status {
		case domain.PelletOpen, domain.PelletInProgress:
			return " ORDER BY p.priority, p.number"
		case domain.PelletMaybeLater:
			return " ORDER BY p.updated_at DESC, p.number DESC"
		case domain.PelletClosed:
			return " ORDER BY p.completed_at DESC, p.number DESC"
		}
	}
	if !options.All {
		return " ORDER BY p.priority, p.number"
	}
	return ` ORDER BY
		CASE
			WHEN p.status IN ('in_progress', 'open') THEN 0
			WHEN p.status = 'maybe_later' THEN 1
			ELSE 2
		END,
		CASE WHEN p.status IN ('in_progress', 'open') THEN p.priority END,
		CASE WHEN p.status = 'maybe_later' THEN p.updated_at END DESC,
		CASE WHEN p.status = 'closed' THEN p.completed_at END DESC,
		CASE WHEN p.status IN ('maybe_later', 'closed') THEN p.number END DESC,
		p.number`
}

func loadPellet(ctx context.Context, query projectQuery, projectID int64, projectCode string, number int64) (storage.Pellet, error) {
	return scanPellet(query.QueryRowContext(ctx, pelletSelect+`
		WHERE p.project_id = ? AND project.code = ? AND p.number = ?`, projectID, projectCode, number))
}

type pelletScanner interface{ Scan(...any) error }

func scanPellet(scanner pelletScanner) (storage.Pellet, error) {
	var pellet storage.Pellet
	var projectCode, status, createdAt, updatedAt string
	var externalID, group sql.NullString
	var priority sql.NullInt64
	var completedAt sql.NullString
	var workspaceID, workspaceProjectID sql.NullInt64
	var rootPath, gitDir, workspaceCreatedAt, workspaceUpdatedAt sql.NullString
	var rootRelative, gitDirRelative sql.NullInt64
	if err := scanner.Scan(
		&pellet.ProjectID, &projectCode, &pellet.Reference.Number,
		&pellet.Title, &pellet.Description, &externalID, &group,
		&status, &priority, &createdAt, &updatedAt, &completedAt,
		&workspaceID, &workspaceProjectID, &rootPath, &rootRelative,
		&gitDir, &gitDirRelative, &workspaceCreatedAt, &workspaceUpdatedAt,
	); err != nil {
		return storage.Pellet{}, err
	}
	pellet.Reference.ProjectCode = projectCode
	pellet.Status = domain.PelletStatus(status)
	pellet.ExternalID = nullableTextPointer(externalID)
	pellet.Group = nullableTextPointer(group)
	if priority.Valid {
		pellet.Priority = &priority.Int64
	}
	var err error
	pellet.CreatedAt, err = parseProjectTimestamp("pellet created_at", createdAt)
	if err != nil {
		return storage.Pellet{}, err
	}
	pellet.UpdatedAt, err = parseProjectTimestamp("pellet updated_at", updatedAt)
	if err != nil {
		return storage.Pellet{}, err
	}
	if completedAt.Valid {
		parsed, parseErr := parseProjectTimestamp("pellet completed_at", completedAt.String)
		if parseErr != nil {
			return storage.Pellet{}, parseErr
		}
		pellet.CompletedAt = &parsed
	}
	if workspaceID.Valid {
		if !workspaceProjectID.Valid || !rootPath.Valid || !rootRelative.Valid || !gitDir.Valid || !gitDirRelative.Valid || !workspaceCreatedAt.Valid || !workspaceUpdatedAt.Valid {
			return storage.Pellet{}, errors.New("incomplete pellet workspace ownership row")
		}
		workspace := storage.Workspace{
			ID: workspaceID.Int64, ProjectID: workspaceProjectID.Int64,
			RootPath: domain.LocalPath{Value: rootPath.String, Relative: rootRelative.Int64 != 0},
			GitDir:   domain.LocalPath{Value: gitDir.String, Relative: gitDirRelative.Int64 != 0},
		}
		workspace.CreatedAt, err = parseProjectTimestamp("pellet workspace created_at", workspaceCreatedAt.String)
		if err != nil {
			return storage.Pellet{}, err
		}
		workspace.UpdatedAt, err = parseProjectTimestamp("pellet workspace updated_at", workspaceUpdatedAt.String)
		if err != nil {
			return storage.Pellet{}, err
		}
		pellet.Workspace = &workspace
	}
	return pellet, nil
}

func validatePelletProjectContext(project storage.ResolvedProject) error {
	if project.Project.ID <= 0 || project.Workspace.ID <= 0 || project.Workspace.ProjectID != project.Project.ID {
		return domain.NewError(domain.Unexpected, "internal_error", "resolved pellet project and workspace context is inconsistent", nil)
	}
	if err := domain.ValidateProjectCode(project.Project.Code); err != nil {
		return domain.NewError(domain.Unexpected, "internal_error", "resolved pellet project code is invalid", nil)
	}
	return nil
}

func validatePelletProject(project storage.Project) error {
	if project.ID <= 0 {
		return domain.NewError(domain.Unexpected, "internal_error", "resolved pellet project context is inconsistent", nil)
	}
	if err := domain.ValidateProjectCode(project.Code); err != nil {
		return domain.NewError(domain.Unexpected, "internal_error", "resolved pellet project code is invalid", nil)
	}
	return nil
}

func validateReferenceProject(project storage.ResolvedProject, reference domain.PelletReference) error {
	if reference.ProjectCode == project.Project.Code && reference.Number > 0 {
		return nil
	}
	if reference.ProjectCode != project.Project.Code {
		return domain.NewError(
			domain.Usage,
			"reference_project_mismatch",
			"the pellet reference belongs to a different logical project",
			map[string]any{
				"reference": reference.String(), "reference_project": reference.ProjectCode,
				"current_project": project.Project.Code,
			},
		)
	}
	return domain.NewError(domain.Usage, "invalid_reference", "the pellet reference number must be positive", map[string]any{"reference": reference.String()})
}

func validateNewPellet(input storage.NewPellet) (storage.NewPellet, error) {
	if input.Status == "" {
		input.Status = domain.PelletOpen
	}
	if input.Status != domain.PelletOpen && input.Status != domain.PelletMaybeLater {
		return storage.NewPellet{}, domain.NewError(
			domain.Usage, "invalid_status", "new pellets may only be open or maybe_later",
			map[string]any{"status": input.Status},
		)
	}
	if input.Placement != nil {
		if input.Status == domain.PelletMaybeLater {
			return storage.NewPellet{}, domain.NewError(
				domain.Usage,
				"conflicting_flags",
				"--maybe-later cannot be combined with --before or --after",
				map[string]any{"flags": []string{"--maybe-later", placementFlag(*input.Placement)}},
			)
		}
		if input.Placement.Target.Number <= 0 {
			return storage.NewPellet{}, domain.NewError(domain.Usage, "invalid_reference", "the placement target must be a valid pellet reference", nil)
		}
	}
	if strings.TrimSpace(input.Title) == "" {
		return storage.NewPellet{}, invalidPelletField("title", "pellet title must not be empty")
	}
	if err := validateNullablePelletText("external_id", input.ExternalID); err != nil {
		return storage.NewPellet{}, err
	}
	if err := validateNullablePelletText("group", input.Group); err != nil {
		return storage.NewPellet{}, err
	}
	return input, nil
}

func validatePelletListOptions(options storage.PelletListOptions) error {
	if options.Status != nil {
		if err := domain.ValidatePelletStatus(*options.Status); err != nil {
			return err
		}
		if options.All {
			return domain.NewError(
				domain.Usage,
				"conflicting_flags",
				"--status and --all are mutually exclusive",
				map[string]any{"flags": []string{"--status", "--all"}},
			)
		}
	}
	if err := validateNullablePelletText("external_id", options.ExternalID); err != nil {
		return err
	}
	if err := validateNullablePelletText("group", options.Group); err != nil {
		return err
	}
	if options.Limit != nil && *options.Limit <= 0 {
		return domain.NewError(domain.Usage, "invalid_limit", "limit must be a positive integer", map[string]any{"limit": *options.Limit})
	}
	return nil
}

func validatePelletSearchOptions(options storage.PelletSearchOptions) error {
	if strings.TrimSpace(options.Query) == "" {
		return domain.NewError(domain.Usage, "missing_query", "search requires a non-empty QUERY", nil)
	}
	if options.Status != nil {
		if err := domain.ValidatePelletStatus(*options.Status); err != nil {
			return err
		}
	}
	if err := validateNullablePelletText("external_id", options.ExternalID); err != nil {
		return err
	}
	if err := validateNullablePelletText("group", options.Group); err != nil {
		return err
	}
	if options.Limit != nil && *options.Limit <= 0 {
		return domain.NewError(domain.Usage, "invalid_limit", "limit must be a positive integer", map[string]any{"limit": *options.Limit})
	}
	return nil
}

func escapeFTS5Query(query string) string {
	terms := strings.Fields(query)
	for index, term := range terms {
		terms[index] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	return strings.Join(terms, " ")
}

func placementFlag(placement storage.PelletPlacement) string {
	if placement.Before {
		return "--before"
	}
	return "--after"
}

func validatePelletChanges(changes storage.PelletChanges) error {
	if changes.Title == nil && changes.Description == nil && !changes.ExternalID.Set && !changes.Group.Set {
		return domain.NewError(domain.Usage, "missing_edit", "at least one editable pellet field is required", nil)
	}
	if changes.Title != nil && strings.TrimSpace(*changes.Title) == "" {
		return invalidPelletField("title", "pellet title must not be empty")
	}
	if changes.ExternalID.Set {
		if err := validateNullablePelletText("external_id", changes.ExternalID.Value); err != nil {
			return err
		}
	}
	if changes.Group.Set {
		if err := validateNullablePelletText("group", changes.Group.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateNullablePelletText(field string, value *string) error {
	if value != nil && *value == "" {
		return invalidPelletField(field, "optional pellet fields must be NULL or non-empty")
	}
	return nil
}

func invalidPelletField(field, message string) error {
	return domain.NewError(domain.Usage, "invalid_pellet_field", message, map[string]any{"field": field})
}

func applyPelletChanges(pellet storage.Pellet, changes storage.PelletChanges) storage.Pellet {
	if changes.Title != nil {
		pellet.Title = *changes.Title
	}
	if changes.Description != nil {
		pellet.Description = *changes.Description
	}
	if changes.ExternalID.Set {
		pellet.ExternalID = changes.ExternalID.Value
	}
	if changes.Group.Set {
		pellet.Group = changes.Group.Value
	}
	return pellet
}

func nullableTextPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableTextValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64Value(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func equalNullableText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func pelletNotFound(reference domain.PelletReference) error {
	return domain.NewError(
		domain.NotFound,
		"pellet_not_found",
		"the pellet does not exist in the selected logical project",
		map[string]any{"reference": reference.String()},
	)
}

func pelletStorageError(operation string, err error) error {
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		primaryCode := sqliteError.Code() & 0xff
		if primaryCode == 5 || primaryCode == 6 {
			return domain.WrapError(
				domain.Conflict, "database_busy", "the Pellets database is busy",
				map[string]any{"operation": operation}, err,
			)
		}
	}
	return domain.WrapError(
		domain.Storage,
		"pellet_storage_failed",
		"could not access pellet records",
		map[string]any{"operation": operation},
		fmt.Errorf("%s: %w", operation, err),
	)
}

func pelletFTSError(operation string, err error) error {
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) {
		primaryCode := sqliteError.Code() & 0xff
		if primaryCode == 5 || primaryCode == 6 {
			return pelletStorageError(operation, err)
		}
	}
	return domain.WrapError(
		domain.Storage,
		"fts_unavailable",
		"pellet full-text search is unavailable",
		map[string]any{"operation": operation},
		err,
	)
}
