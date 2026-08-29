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

	var number int64
	if err := connection.QueryRowContext(ctx, `
		UPDATE projects
		SET next_pellet_number = next_pellet_number + 1,
		    updated_at = ?
		WHERE project_id = ?
		RETURNING next_pellet_number - 1`, timestamp, project.Project.ID).Scan(&number); err != nil {
		return storage.Pellet{}, pelletStorageError("allocate pellet number", err)
	}

	var priority *int64
	if normalized.Status == domain.PelletOpen {
		allocated, allocationErr := allocateTailPriority(ctx, connection, project.Project.ID)
		if allocationErr != nil {
			return storage.Pellet{}, allocationErr
		}
		priority = &allocated
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
