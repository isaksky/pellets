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

const projectColumns = `project_id, code, git_common_dir, git_common_dir_relative,
	strftime('%Y-%m-%dT%H:%M:%fZ', created_at),
	strftime('%Y-%m-%dT%H:%M:%fZ', updated_at)`

const workspaceColumns = `workspace_id, project_id, root_path, root_path_relative,
	git_dir, git_dir_relative,
	strftime('%Y-%m-%dT%H:%M:%fZ', created_at),
	strftime('%Y-%m-%dT%H:%M:%fZ', updated_at)`

// ProjectDatabase owns a configured SQLite database used for logical-project
// and Git-worktree workspace registration and queries.
type ProjectDatabase struct {
	db *sql.DB
}

func OpenProjectDatabase(ctx context.Context, databasePath string) (*ProjectDatabase, error) {
	db, err := Open(ctx, databasePath)
	if err != nil {
		return nil, err
	}
	return &ProjectDatabase{db: db}, nil
}

func (database *ProjectDatabase) Close() error { return database.db.Close() }

// RegisterProject atomically creates a logical repository or attaches its
// current workspace. All Git and filesystem checks have already completed.
func (database *ProjectDatabase) RegisterProject(ctx context.Context, registration storage.ProjectRegistration) (storage.Project, bool, error) {
	if err := domain.ValidateProjectCode(registration.Code); err != nil {
		return storage.Project{}, false, err
	}
	for label, localPath := range map[string]domain.LocalPath{
		"git_common_dir": registration.GitCommonDir,
		"root_path":      registration.WorkspaceRoot,
		"git_dir":        registration.GitDir,
	} {
		if err := validateStoredLocalPath(label, localPath); err != nil {
			return storage.Project{}, false, err
		}
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

	byRepository, repositoryErr := findProjectRow(ctx, connection,
		"git_common_dir_relative = ? AND git_common_dir = ?",
		boolInt(registration.GitCommonDir.Relative), registration.GitCommonDir.Value)
	if repositoryErr != nil && !errors.Is(repositoryErr, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up Git repository identity", repositoryErr)
	}
	byCode, codeErr := findProjectRow(ctx, connection, "code = ?", registration.Code)
	if codeErr != nil && !errors.Is(codeErr, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up project code", codeErr)
	}

	if repositoryErr == nil && byRepository.Code != registration.Code {
		return storage.Project{}, false, domain.NewError(
			domain.Conflict,
			"project_repository_already_registered",
			"the Git repository is already registered with a different immutable code",
			map[string]any{"existing_code": byRepository.Code, "requested_code": registration.Code},
		)
	}
	if codeErr == nil && (repositoryErr != nil || byCode.ID != byRepository.ID) {
		return storage.Project{}, false, domain.NewError(
			domain.Conflict,
			"project_code_already_registered",
			"the project code is already registered for a different Git repository",
			map[string]any{"code": registration.Code},
		)
	}

	byRoot, rootErr := findWorkspaceRow(ctx, connection,
		"root_path_relative = ? AND root_path = ?",
		boolInt(registration.WorkspaceRoot.Relative), registration.WorkspaceRoot.Value)
	if rootErr != nil && !errors.Is(rootErr, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up workspace root", rootErr)
	}
	byGitDir, gitDirErr := findWorkspaceRow(ctx, connection,
		"git_dir_relative = ? AND git_dir = ?",
		boolInt(registration.GitDir.Relative), registration.GitDir.Value)
	if gitDirErr != nil && !errors.Is(gitDirErr, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up workspace Git directory", gitDirErr)
	}

	if rootErr == nil {
		if gitDirErr != nil || byRoot.ID != byGitDir.ID || repositoryErr != nil || byRoot.ProjectID != byRepository.ID {
			return storage.Project{}, false, workspaceIdentityConflict(registration, byRoot, byGitDir)
		}
		project, err := loadProject(ctx, connection, byRepository.ID)
		if err != nil {
			return storage.Project{}, false, projectStorageError("read idempotent project registration", err)
		}
		if err := commitProjectRegistration(ctx, connection, "idempotent"); err != nil {
			return storage.Project{}, false, err
		}
		committed = true
		return project, false, nil
	}

	if gitDirErr == nil {
		if repositoryErr != nil || byGitDir.ProjectID != byRepository.ID || !registration.AllowWorkspaceMove {
			return storage.Project{}, false, workspaceIdentityConflict(registration, storage.Workspace{}, byGitDir)
		}
		var timestamp float64
		if err := connection.QueryRowContext(ctx, "SELECT julianday('now')").Scan(&timestamp); err != nil {
			return storage.Project{}, false, projectStorageError("capture workspace move timestamp", err)
		}
		if _, err := connection.ExecContext(ctx, `
			UPDATE project_workspaces
			SET root_path = ?, root_path_relative = ?, updated_at = ?
			WHERE workspace_id = ?`, registration.WorkspaceRoot.Value, boolInt(registration.WorkspaceRoot.Relative), timestamp, byGitDir.ID); err != nil {
			return storage.Project{}, false, projectStorageError("update moved workspace", err)
		}
		if _, err := connection.ExecContext(ctx, "UPDATE projects SET updated_at = ? WHERE project_id = ?", timestamp, byRepository.ID); err != nil {
			return storage.Project{}, false, projectStorageError("update project after workspace move", err)
		}
		project, err := loadProject(ctx, connection, byRepository.ID)
		if err != nil {
			return storage.Project{}, false, projectStorageError("read project after workspace move", err)
		}
		if err := commitProjectRegistration(ctx, connection, "workspace move"); err != nil {
			return storage.Project{}, false, err
		}
		committed = true
		return project, false, nil
	}

	var timestamp float64
	if err := connection.QueryRowContext(ctx, "SELECT julianday('now')").Scan(&timestamp); err != nil {
		return storage.Project{}, false, projectStorageError("capture project registration timestamp", err)
	}
	projectID := byRepository.ID
	if repositoryErr != nil {
		result, err := connection.ExecContext(ctx, `
			INSERT INTO projects(code, git_common_dir, git_common_dir_relative, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, registration.Code, registration.GitCommonDir.Value,
			boolInt(registration.GitCommonDir.Relative), timestamp, timestamp)
		if err != nil {
			return storage.Project{}, false, projectStorageError("insert logical project", err)
		}
		projectID, err = result.LastInsertId()
		if err != nil {
			return storage.Project{}, false, projectStorageError("read logical project identity", err)
		}
	} else {
		if _, err := connection.ExecContext(ctx, "UPDATE projects SET updated_at = ? WHERE project_id = ?", timestamp, projectID); err != nil {
			return storage.Project{}, false, projectStorageError("update project for workspace attachment", err)
		}
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO project_workspaces(
			project_id, root_path, root_path_relative, git_dir, git_dir_relative, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, registration.WorkspaceRoot.Value, boolInt(registration.WorkspaceRoot.Relative),
		registration.GitDir.Value, boolInt(registration.GitDir.Relative), timestamp, timestamp); err != nil {
		return storage.Project{}, false, projectStorageError("insert project workspace", err)
	}
	project, err := loadProject(ctx, connection, projectID)
	if err != nil {
		return storage.Project{}, false, projectStorageError("read registered project", err)
	}
	if err := commitProjectRegistration(ctx, connection, "project registration"); err != nil {
		return storage.Project{}, false, err
	}
	committed = true
	return project, true, nil
}

func commitProjectRegistration(ctx context.Context, connection *sql.Conn, action string) error {
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return projectStorageError("commit "+action, err)
	}
	return nil
}

func (database *ProjectDatabase) ListProjects(ctx context.Context) ([]storage.Project, error) {
	rows, err := database.db.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY code")
	if err != nil {
		return nil, projectStorageError("list projects", err)
	}
	var projects []storage.Project
	for rows.Next() {
		project, scanErr := scanProject(rows)
		if scanErr != nil {
			rows.Close()
			return nil, projectStorageError("read project list", scanErr)
		}
		projects = append(projects, project)
	}
	if err := rows.Close(); err != nil {
		return nil, projectStorageError("close project list", err)
	}
	if err := rows.Err(); err != nil {
		return nil, projectStorageError("read project list", err)
	}
	for index := range projects {
		workspaces, err := loadWorkspaces(ctx, database.db, projects[index].ID)
		if err != nil {
			return nil, projectStorageError("read project workspaces", err)
		}
		projects[index].Workspaces = workspaces
	}
	if projects == nil {
		projects = make([]storage.Project, 0)
	}
	return projects, nil
}

func (database *ProjectDatabase) FindProjectByCode(ctx context.Context, code string) (storage.Project, error) {
	project, err := findProjectRow(ctx, database.db, "code = ?", code)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Project{}, projectNotFound(map[string]any{"code": code})
	}
	if err != nil {
		return storage.Project{}, projectStorageError("find project by code", err)
	}
	project.Workspaces, err = loadWorkspaces(ctx, database.db, project.ID)
	if err != nil {
		return storage.Project{}, projectStorageError("read project workspaces", err)
	}
	return project, nil
}

func (database *ProjectDatabase) FindWorkspaceByGitDir(ctx context.Context, gitDir domain.LocalPath) (storage.ResolvedProject, error) {
	workspace, err := findWorkspaceRow(ctx, database.db,
		"git_dir_relative = ? AND git_dir = ?", boolInt(gitDir.Relative), gitDir.Value)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ResolvedProject{}, workspaceNotFound(map[string]any{"git_dir": gitDir.Value})
	}
	if err != nil {
		return storage.ResolvedProject{}, projectStorageError("find workspace by Git directory", err)
	}
	project, err := loadProject(ctx, database.db, workspace.ProjectID)
	if err != nil {
		return storage.ResolvedProject{}, projectStorageError("read workspace project", err)
	}
	return storage.ResolvedProject{Project: project, Workspace: workspace}, nil
}

func (database *ProjectDatabase) ResolveProjectWorkspace(ctx context.Context, commonDir, rootPath, gitDir domain.LocalPath) (storage.ResolvedProject, error) {
	workspace, err := scanWorkspace(database.db.QueryRowContext(ctx, `
		SELECT w.workspace_id, w.project_id, w.root_path, w.root_path_relative,
		       w.git_dir, w.git_dir_relative,
		       strftime('%Y-%m-%dT%H:%M:%fZ', w.created_at),
		       strftime('%Y-%m-%dT%H:%M:%fZ', w.updated_at)
		FROM project_workspaces AS w
		JOIN projects AS p USING (project_id)
		WHERE p.git_common_dir_relative = ? AND p.git_common_dir = ?
		  AND w.root_path_relative = ? AND w.root_path = ?
		  AND w.git_dir_relative = ? AND w.git_dir = ?`,
		boolInt(commonDir.Relative), commonDir.Value,
		boolInt(rootPath.Relative), rootPath.Value,
		boolInt(gitDir.Relative), gitDir.Value))
	if err == nil {
		project, loadErr := loadProject(ctx, database.db, workspace.ProjectID)
		if loadErr != nil {
			return storage.ResolvedProject{}, projectStorageError("read resolved logical project", loadErr)
		}
		return storage.ResolvedProject{Project: project, Workspace: workspace}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.ResolvedProject{}, projectStorageError("resolve current project workspace", err)
	}

	project, projectErr := findProjectRow(ctx, database.db,
		"git_common_dir_relative = ? AND git_common_dir = ?", boolInt(commonDir.Relative), commonDir.Value)
	if errors.Is(projectErr, sql.ErrNoRows) {
		return storage.ResolvedProject{}, projectNotFound(map[string]any{"git_common_dir": commonDir.Value})
	}
	if projectErr != nil {
		return storage.ResolvedProject{}, projectStorageError("resolve current logical project", projectErr)
	}
	return storage.ResolvedProject{}, workspaceNotFound(map[string]any{
		"project": project.Code, "root_path": rootPath.Value, "git_dir": gitDir.Value,
	})
}

type projectQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type rowsQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func findProjectRow(ctx context.Context, query projectQuery, predicate string, args ...any) (storage.Project, error) {
	return scanProject(query.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE "+predicate, args...))
}

func findWorkspaceRow(ctx context.Context, query projectQuery, predicate string, args ...any) (storage.Workspace, error) {
	return scanWorkspace(query.QueryRowContext(ctx, "SELECT "+workspaceColumns+" FROM project_workspaces WHERE "+predicate, args...))
}

func loadProject(ctx context.Context, query interface {
	projectQuery
	rowsQuery
}, projectID int64) (storage.Project, error) {
	project, err := findProjectRow(ctx, query, "project_id = ?", projectID)
	if err != nil {
		return storage.Project{}, err
	}
	project.Workspaces, err = loadWorkspaces(ctx, query, projectID)
	return project, err
}

func loadWorkspaces(ctx context.Context, query rowsQuery, projectID int64) ([]storage.Workspace, error) {
	rows, err := query.QueryContext(ctx, "SELECT "+workspaceColumns+" FROM project_workspaces WHERE project_id = ? ORDER BY workspace_id", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workspaces := make([]storage.Workspace, 0)
	for rows.Next() {
		workspace, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

type projectScanner interface{ Scan(...any) error }

func scanProject(scanner projectScanner) (storage.Project, error) {
	var project storage.Project
	var commonRelative int
	var createdAt, updatedAt string
	if err := scanner.Scan(&project.ID, &project.Code, &project.GitCommonDir.Value, &commonRelative, &createdAt, &updatedAt); err != nil {
		return storage.Project{}, err
	}
	project.GitCommonDir.Relative = commonRelative != 0
	var err error
	project.CreatedAt, err = parseProjectTimestamp("project created_at", createdAt)
	if err != nil {
		return storage.Project{}, err
	}
	project.UpdatedAt, err = parseProjectTimestamp("project updated_at", updatedAt)
	return project, err
}

func scanWorkspace(scanner projectScanner) (storage.Workspace, error) {
	var workspace storage.Workspace
	var rootRelative, gitDirRelative int
	var createdAt, updatedAt string
	if err := scanner.Scan(
		&workspace.ID, &workspace.ProjectID,
		&workspace.RootPath.Value, &rootRelative,
		&workspace.GitDir.Value, &gitDirRelative,
		&createdAt, &updatedAt,
	); err != nil {
		return storage.Workspace{}, err
	}
	workspace.RootPath.Relative = rootRelative != 0
	workspace.GitDir.Relative = gitDirRelative != 0
	var err error
	workspace.CreatedAt, err = parseProjectTimestamp("workspace created_at", createdAt)
	if err != nil {
		return storage.Workspace{}, err
	}
	workspace.UpdatedAt, err = parseProjectTimestamp("workspace updated_at", updatedAt)
	return workspace, err
}

func parseProjectTimestamp(label, value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s %q: %w", label, value, err)
	}
	return parsed, nil
}

func validateStoredLocalPath(label string, localPath domain.LocalPath) error {
	value := localPath.Value
	invalid := value == "" || strings.Contains(value, `\`) || path.Clean(value) != value
	if localPath.Relative {
		invalid = invalid || path.IsAbs(value) || value == ".." || strings.HasPrefix(value, "../")
	} else {
		// filepath-style drive paths are slash-normalized but path.IsAbs does
		// not recognize them on Unix. Requiring either slash-rooted or a drive
		// prefix keeps malformed relative values out without platform coupling.
		invalid = invalid || !(strings.HasPrefix(value, "/") || len(value) >= 3 && value[1] == ':' && value[2] == '/')
	}
	if invalid {
		return domain.NewError(
			domain.Usage,
			"invalid_workspace_path",
			"Git repository and workspace paths must be normalized slash-separated local paths",
			map[string]any{"field": label, "path": value, "relative": localPath.Relative},
		)
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func projectNotFound(details map[string]any) error {
	return domain.NewError(domain.NotFound, "project_not_registered", "the Git repository is not registered in the selected Pellets database", details)
}

func workspaceNotFound(details map[string]any) error {
	return domain.NewError(domain.NotFound, "workspace_not_registered", "the current Git worktree is not registered in the selected Pellets project", details)
}

func workspaceIdentityConflict(registration storage.ProjectRegistration, byRoot, byGitDir storage.Workspace) error {
	details := map[string]any{
		"requested_root_path": registration.WorkspaceRoot.Value,
		"requested_git_dir":   registration.GitDir.Value,
	}
	if byRoot.ID != 0 {
		details["root_workspace_id"] = byRoot.ID
	}
	if byGitDir.ID != 0 {
		details["git_dir_workspace_id"] = byGitDir.ID
	}
	return domain.NewError(
		domain.Conflict,
		"workspace_identity_conflict",
		"the Git worktree root and Git directory do not identify one available workspace",
		details,
	)
}

func projectStorageError(operation string, err error) error {
	return domain.WrapError(domain.Storage, "project_storage_failed", "could not access project registrations", map[string]any{"operation": operation}, err)
}
