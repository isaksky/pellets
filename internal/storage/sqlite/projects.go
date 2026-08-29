package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

const projectColumns = `code, root_path,
	strftime('%Y-%m-%dT%H:%M:%fZ', created_at),
	strftime('%Y-%m-%dT%H:%M:%fZ', updated_at)`

// ProjectDatabase owns a configured SQLite database used for project
// registration and queries.
type ProjectDatabase struct {
	db *sql.DB
}

// OpenProjectDatabase opens the selected database and exposes its project
// persistence operations.
func OpenProjectDatabase(ctx context.Context, databasePath string) (*ProjectDatabase, error) {
	db, err := Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	return &ProjectDatabase{db: db}, nil
}

// Close releases the selected database.
func (database *ProjectDatabase) Close() error {
	return database.db.Close()
}

// RegisterProject atomically registers a normalized path and immutable code.
// An exact repeat returns the existing row with created=false.
func (database *ProjectDatabase) RegisterProject(ctx context.Context, code, rootPath string) (storage.Project, bool, error) {
	if err := domain.ValidateProjectCode(code); err != nil {
		return storage.Project{}, false, err
	}
	if err := validateStoredProjectPath(rootPath); err != nil {
		return storage.Project{}, false, err
	}

	connection, err := database.db.Conn(ctx)
	if err != nil {
		return storage.Project{}, false, projectStorageError("open project registration connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.Project{}, false, projectStorageError("begin project registration", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	project, err := findProject(ctx, connection, "root_path", rootPath)
	if err == nil {
		if project.Code != code {
			return storage.Project{}, false, domain.NewError(
				domain.Conflict,
				"project_path_already_registered",
				"the Git work tree is already registered with a different immutable code",
				map[string]any{"root_path": rootPath, "existing_code": project.Code, "requested_code": code},
			)
		}
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			return storage.Project{}, false, projectStorageError("commit idempotent project registration", err)
		}
		committed = true
		return project, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up registered project path", err)
	}

	existingCode, err := findProject(ctx, connection, "code", code)
	if err == nil {
		return storage.Project{}, false, domain.NewError(
			domain.Conflict,
			"project_code_already_registered",
			"the project code is already registered for a different Git work tree",
			map[string]any{"code": code, "existing_root_path": existingCode.RootPath, "requested_root_path": rootPath},
		)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up registered project code", err)
	}

	var timestamp float64
	if err := connection.QueryRowContext(ctx, "SELECT julianday('now')").Scan(&timestamp); err != nil {
		return storage.Project{}, false, projectStorageError("capture project registration timestamp", err)
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO projects(code, root_path, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, code, rootPath, timestamp, timestamp); err != nil {
		return storage.Project{}, false, projectStorageError("insert project registration", err)
	}
	project, err = findProject(ctx, connection, "code", code)
	if err != nil {
		return storage.Project{}, false, projectStorageError("read registered project", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.Project{}, false, projectStorageError("commit project registration", err)
	}
	committed = true
	return project, true, nil
}

// ListProjects returns all projects in stable code order.
func (database *ProjectDatabase) ListProjects(ctx context.Context) ([]storage.Project, error) {
	rows, err := database.db.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY code")
	if err != nil {
		return nil, projectStorageError("list projects", err)
	}
	defer rows.Close()

	projects := make([]storage.Project, 0)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, projectStorageError("read project list", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, projectStorageError("read project list", err)
	}
	return projects, nil
}

// FindProjectByCode selects a project by its immutable public code.
func (database *ProjectDatabase) FindProjectByCode(ctx context.Context, code string) (storage.Project, error) {
	project, err := findProject(ctx, database.db, "code", code)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Project{}, projectNotFound(map[string]any{"code": code})
	}
	if err != nil {
		return storage.Project{}, projectStorageError("find project by code", err)
	}
	return project, nil
}

// FindProjectByRootPath selects the registered project for a normalized root.
func (database *ProjectDatabase) FindProjectByRootPath(ctx context.Context, rootPath string) (storage.Project, error) {
	project, err := findProject(ctx, database.db, "root_path", rootPath)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Project{}, projectNotFound(map[string]any{"root_path": rootPath})
	}
	if err != nil {
		return storage.Project{}, projectStorageError("find project by root path", err)
	}
	return project, nil
}

type projectQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func findProject(ctx context.Context, query projectQuery, column, value string) (storage.Project, error) {
	if column != "code" && column != "root_path" {
		return storage.Project{}, fmt.Errorf("unsupported project lookup column %q", column)
	}
	return scanProject(query.QueryRowContext(
		ctx,
		"SELECT "+projectColumns+" FROM projects WHERE "+column+" = ?",
		value,
	))
}

type projectScanner interface {
	Scan(...any) error
}

func scanProject(scanner projectScanner) (storage.Project, error) {
	var project storage.Project
	var createdAt, updatedAt string
	if err := scanner.Scan(&project.Code, &project.RootPath, &createdAt, &updatedAt); err != nil {
		return storage.Project{}, err
	}
	var err error
	project.CreatedAt, err = time.Parse("2006-01-02T15:04:05.000Z", createdAt)
	if err != nil {
		return storage.Project{}, fmt.Errorf("parse project created_at %q: %w", createdAt, err)
	}
	project.UpdatedAt, err = time.Parse("2006-01-02T15:04:05.000Z", updatedAt)
	if err != nil {
		return storage.Project{}, fmt.Errorf("parse project updated_at %q: %w", updatedAt, err)
	}
	return project, nil
}

func validateStoredProjectPath(rootPath string) error {
	if rootPath == "" || strings.Contains(rootPath, `\`) || path.IsAbs(rootPath) || path.Clean(rootPath) != rootPath || rootPath == ".." || strings.HasPrefix(rootPath, "../") {
		return domain.NewError(
			domain.Usage,
			"invalid_project_path",
			"project root path must be a normalized slash-separated path relative to the database root",
			map[string]any{"root_path": rootPath},
		)
	}
	return nil
}

func projectNotFound(details map[string]any) error {
	return domain.NewError(
		domain.NotFound,
		"project_not_registered",
		"the requested Git project is not registered in the selected Pellets database",
		details,
	)
}

func projectStorageError(operation string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"project_storage_failed",
		"could not access project registrations",
		map[string]any{"operation": operation},
		err,
	)
}
