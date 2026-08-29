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
			allocated, err = allocatePlacedPriority(ctx, connection, project, *normalized.Placement)
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
		return storage.Pellet{}, pelletStorageError("index inserted pellet", err)
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
			return storage.Pellet{}, pelletStorageError("remove old pellet search text", err)
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
			return storage.Pellet{}, pelletStorageError("index updated pellet", err)
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

func allocatePlacedPriority(ctx context.Context, query projectQuery, project storage.ResolvedProject, placement storage.PelletPlacement) (int64, error) {
	if err := validateReferenceProject(project, placement.Target); err != nil {
		return 0, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		priority, available, err := placedPriority(ctx, query, project.Project.ID, placement)
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

func placedPriority(ctx context.Context, query projectQuery, projectID int64, placement storage.PelletPlacement) (int64, bool, error) {
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
			WHERE project_id = ? AND priority < ?`, projectID, priority).Scan(&previous); err != nil {
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
		WHERE project_id = ? AND priority > ?`, projectID, priority).Scan(&next); err != nil {
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
	if _, err := query.ExecContext(ctx, `
		WITH
		bounds AS MATERIALIZED (
			SELECT ? AS base
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
		WHERE p.project_id = ? AND p.priority IS NOT NULL`, base, stride, projectID, projectID); err != nil {
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
