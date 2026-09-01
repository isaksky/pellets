package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"slices"
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

const projectRedirectColumns = `code, project_id,
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
	if registration.GenerateCode {
		if registration.CodeName == "" || registration.CodeIdentity == "" {
			return storage.Project{}, false, domain.NewError(
				domain.Unexpected, "internal_error", "automatic project code source is incomplete", nil)
		}
		registration.Code = domain.GenerateProjectCode(registration.CodeName, registration.CodeIdentity, false, 0)
	} else {
		if err := domain.ValidateProjectCode(registration.Code); err != nil {
			return storage.Project{}, false, err
		}
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
	if repositoryErr == nil && registration.GenerateCode {
		registration.Code = byRepository.Code
	}
	byCode, codeErr := findProjectByAnyCodeRow(ctx, connection, registration.Code)
	if codeErr != nil && !errors.Is(codeErr, sql.ErrNoRows) {
		return storage.Project{}, false, projectStorageError("look up project code", codeErr)
	}

	if repositoryErr == nil && byRepository.Code != registration.Code {
		if codeErr == nil && byCode.ID == byRepository.ID {
			// A former code for this same stable project is a valid selection,
			// but registration and every successful result remain canonical.
			registration.Code = byRepository.Code
		} else {
			return storage.Project{}, false, domain.NewError(
				domain.Conflict,
				"project_repository_already_registered",
				"the Git repository is already registered with a different canonical code",
				map[string]any{"existing_code": byRepository.Code, "requested_code": registration.Code},
			)
		}
	}
	if registration.GenerateCode && repositoryErr != nil && codeErr == nil {
		for attempt := uint64(0); ; attempt++ {
			candidate := domain.GenerateProjectCode(registration.CodeName, registration.CodeIdentity, true, attempt)
			if candidate == registration.Code {
				continue
			}
			candidateProject, candidateErr := findProjectByAnyCodeRow(ctx, connection, candidate)
			if errors.Is(candidateErr, sql.ErrNoRows) {
				registration.Code = candidate
				codeErr = candidateErr
				break
			}
			if candidateErr != nil {
				return storage.Project{}, false, projectStorageError("look up generated project code", candidateErr)
			}
			_ = candidateProject
		}
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
		redirects, err := loadProjectCodeRedirects(ctx, database.db, projects[index].ID)
		if err != nil {
			return nil, projectStorageError("read project code redirects", err)
		}
		projects[index].Redirects = redirects
	}
	if projects == nil {
		projects = make([]storage.Project, 0)
	}
	return projects, nil
}

func (database *ProjectDatabase) FindProjectByCode(ctx context.Context, code string) (storage.Project, error) {
	project, err := findProjectByAnyCodeRow(ctx, database.db, code)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Project{}, projectNotFound(map[string]any{"code": code})
	}
	if err != nil {
		return storage.Project{}, projectStorageError("find project by code", err)
	}
	project, err = loadProject(ctx, database.db, project.ID)
	if err != nil {
		return storage.Project{}, projectStorageError("read resolved project", err)
	}
	return project, nil
}

// PlanProjectRename performs the write-free lookup used before a human prompt
// or an automation-safe confirmation-required result.
func (database *ProjectDatabase) PlanProjectRename(ctx context.Context, projectID int64, newCode string) (storage.ProjectRenamePlan, error) {
	if projectID <= 0 {
		return storage.ProjectRenamePlan{}, domain.NewError(domain.Unexpected, "internal_error", "resolved project identity is invalid", nil)
	}
	if err := domain.ValidateProjectCode(newCode); err != nil {
		return storage.ProjectRenamePlan{}, err
	}
	plan, err := planProjectRename(ctx, database.db, projectID, newCode)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ProjectRenamePlan{}, projectNotFound(map[string]any{"project_id": projectID})
	}
	if err != nil {
		return storage.ProjectRenamePlan{}, projectStorageError("plan project rename", err)
	}
	return plan, nil
}

// RenameProject revalidates the complete displayed conflict set and applies
// redirect deletion, canonical-code update, and former-code insertion in one
// immediate transaction.
func (database *ProjectDatabase) RenameProject(ctx context.Context, request storage.ProjectRenameRequest) (storage.ProjectRenameResult, error) {
	if request.ProjectID <= 0 {
		return storage.ProjectRenameResult{}, domain.NewError(domain.Unexpected, "internal_error", "resolved project identity is invalid", nil)
	}
	if err := domain.ValidateProjectCode(request.NewCode); err != nil {
		return storage.ProjectRenameResult{}, err
	}
	for _, conflict := range request.ExpectedConflictingRedirects {
		if err := domain.ValidateProjectCode(conflict.Code); err != nil || conflict.ProjectID <= 0 || domain.ValidateProjectCode(conflict.CanonicalCode) != nil {
			return storage.ProjectRenameResult{}, domain.NewError(domain.Unexpected, "internal_error", "expected project redirect conflict is invalid", nil)
		}
	}

	connection, err := database.db.Conn(ctx)
	if err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("open project rename connection", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("begin project rename", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	plan, err := planProjectRename(ctx, connection, request.ProjectID, request.NewCode)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ProjectRenameResult{}, projectNotFound(map[string]any{"project_id": request.ProjectID})
	}
	if err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("revalidate project rename", err)
	}
	if len(plan.Conflicts) > 0 && !request.DeleteConflictingRedirects {
		return storage.ProjectRenameResult{}, projectRenameConfirmationRequired(plan)
	}
	if request.DeleteConflictingRedirects {
		if !slices.Equal(plan.Conflicts, request.ExpectedConflictingRedirects) {
			return storage.ProjectRenameResult{}, projectRenameConflictSetChanged(request.ExpectedConflictingRedirects, plan.Conflicts)
		}
	} else if len(request.ExpectedConflictingRedirects) > 0 {
		return storage.ProjectRenameResult{}, projectRenameConflictSetChanged(request.ExpectedConflictingRedirects, plan.Conflicts)
	}

	result := storage.ProjectRenameResult{Project: plan.Project, PreviousCode: plan.Project.Code}
	if plan.Project.Code == request.NewCode {
		if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
			return storage.ProjectRenameResult{}, projectStorageError("commit idempotent project rename", err)
		}
		committed = true
		return result, nil
	}

	if request.DeleteConflictingRedirects {
		for _, conflict := range plan.Conflicts {
			changed, err := connection.ExecContext(ctx, `
				DELETE FROM project_code_redirects
				WHERE code = ? AND project_id = ?`, conflict.Code, conflict.ProjectID)
			if err != nil {
				return storage.ProjectRenameResult{}, projectStorageError("delete confirmed project code redirect", err)
			}
			rows, err := changed.RowsAffected()
			if err != nil || rows != 1 {
				if err == nil {
					err = fmt.Errorf("deleted %d redirect rows, want 1", rows)
				}
				return storage.ProjectRenameResult{}, projectStorageError("verify confirmed redirect deletion", err)
			}
		}
		result.RemovedConflicts = append([]storage.ProjectCodeConflict(nil), plan.Conflicts...)
	}

	// Promotion of a redirect already owned by this project never needs
	// confirmation. A conflicting redirect was removed above after revalidation.
	if _, err := connection.ExecContext(ctx, `
		DELETE FROM project_code_redirects
		WHERE code = ? AND project_id = ?`, request.NewCode, request.ProjectID); err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("remove promoted project code redirect", err)
	}
	timestamp, err := captureJulianTimestamp(ctx, connection)
	if err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("capture project rename timestamp", err)
	}
	changed, err := connection.ExecContext(ctx, `
		UPDATE projects SET code = ?, updated_at = ?
		WHERE project_id = ? AND code = ?`, request.NewCode, timestamp, request.ProjectID, plan.Project.Code)
	if err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("update canonical project code", err)
	}
	rows, err := changed.RowsAffected()
	if err != nil || rows != 1 {
		if err == nil {
			err = fmt.Errorf("updated %d project rows, want 1", rows)
		}
		return storage.ProjectRenameResult{}, projectStorageError("verify canonical project update", err)
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO project_code_redirects(code, project_id, created_at, updated_at)
		VALUES (?, ?, ?, ?)`, plan.Project.Code, request.ProjectID, timestamp, timestamp); err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("preserve former canonical project code", err)
	}
	result.Project, err = loadProject(ctx, connection, request.ProjectID)
	if err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("read renamed project", err)
	}
	result.Changed = true
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return storage.ProjectRenameResult{}, projectStorageError("commit project rename", err)
	}
	committed = true
	return result, nil
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

// findProjectByAnyCodeRow deliberately performs at most one redirect lookup.
// Redirect rows target projects.project_id directly, so no recursive traversal
// or redirect chain can occur.
func findProjectByAnyCodeRow(ctx context.Context, query projectQuery, code string) (storage.Project, error) {
	project, err := findProjectRow(ctx, query, "code = ?", code)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return project, err
	}
	return findProjectRow(ctx, query, `project_id = (
		SELECT project_id FROM project_code_redirects WHERE code = ?
	)`, code)
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
	if err != nil {
		return storage.Project{}, err
	}
	project.Redirects, err = loadProjectCodeRedirects(ctx, query, projectID)
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

func loadProjectCodeRedirects(ctx context.Context, query rowsQuery, projectID int64) ([]storage.ProjectCodeRedirect, error) {
	rows, err := query.QueryContext(ctx, "SELECT "+projectRedirectColumns+" FROM project_code_redirects WHERE project_id = ? ORDER BY code", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	redirects := make([]storage.ProjectCodeRedirect, 0)
	for rows.Next() {
		redirect, err := scanProjectCodeRedirect(rows)
		if err != nil {
			return nil, err
		}
		redirects = append(redirects, redirect)
	}
	return redirects, rows.Err()
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

func scanProjectCodeRedirect(scanner projectScanner) (storage.ProjectCodeRedirect, error) {
	var redirect storage.ProjectCodeRedirect
	var createdAt, updatedAt string
	if err := scanner.Scan(&redirect.Code, &redirect.ProjectID, &createdAt, &updatedAt); err != nil {
		return storage.ProjectCodeRedirect{}, err
	}
	var err error
	redirect.CreatedAt, err = parseProjectTimestamp("project redirect created_at", createdAt)
	if err != nil {
		return storage.ProjectCodeRedirect{}, err
	}
	redirect.UpdatedAt, err = parseProjectTimestamp("project redirect updated_at", updatedAt)
	return redirect, err
}

func planProjectRename(ctx context.Context, query interface {
	projectQuery
	rowsQuery
}, projectID int64, newCode string) (storage.ProjectRenamePlan, error) {
	project, err := loadProject(ctx, query, projectID)
	if err != nil {
		return storage.ProjectRenamePlan{}, err
	}
	plan := storage.ProjectRenamePlan{
		Project: project, NewCode: newCode, Conflicts: make([]storage.ProjectCodeConflict, 0),
	}
	if project.Code == newCode {
		return plan, nil
	}

	canonical, err := findProjectRow(ctx, query, "code = ?", newCode)
	if err == nil {
		return storage.ProjectRenamePlan{}, domain.NewError(
			domain.Conflict,
			"project_code_already_registered",
			"the requested code is the canonical code of another project",
			map[string]any{
				"requested_code":    newCode,
				"canonical_project": canonical.Code,
				"project_id":        canonical.ID,
			},
		)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return storage.ProjectRenamePlan{}, err
	}

	var redirectProjectID int64
	var canonicalCode string
	err = query.QueryRowContext(ctx, `
		SELECT redirect.project_id, project.code
		FROM project_code_redirects AS redirect
		JOIN projects AS project ON project.project_id = redirect.project_id
		WHERE redirect.code = ?`, newCode).Scan(&redirectProjectID, &canonicalCode)
	if errors.Is(err, sql.ErrNoRows) {
		return plan, nil
	}
	if err != nil {
		return storage.ProjectRenamePlan{}, err
	}
	if redirectProjectID != projectID {
		plan.Conflicts = append(plan.Conflicts, storage.ProjectCodeConflict{
			Code: newCode, ProjectID: redirectProjectID, CanonicalCode: canonicalCode,
		})
	}
	return plan, nil
}

func projectRenameConfirmationRequired(plan storage.ProjectRenamePlan) error {
	return domain.NewError(
		domain.Confirmation,
		"project_rename_confirmation_required",
		"renaming requires explicit confirmation before conflicting redirect rules can be deleted",
		map[string]any{
			"project":   plan.Project.Code,
			"new_code":  plan.NewCode,
			"conflicts": projectCodeConflictDetails(plan.Conflicts),
			"retry":     []string{"--delete-conflicting-redirects", "--yes"},
		},
	)
}

func projectRenameConflictSetChanged(expected, actual []storage.ProjectCodeConflict) error {
	return domain.NewError(
		domain.Conflict,
		"project_redirect_conflicts_changed",
		"the conflicting project redirect rules changed before the rename could be applied",
		map[string]any{
			"expected_conflicts": projectCodeConflictDetails(expected),
			"actual_conflicts":   projectCodeConflictDetails(actual),
		},
	)
}

func projectCodeConflictDetails(conflicts []storage.ProjectCodeConflict) []map[string]any {
	details := make([]map[string]any, len(conflicts))
	for index, conflict := range conflicts {
		details[index] = map[string]any{
			"code":             conflict.Code,
			"project_id":       conflict.ProjectID,
			"canonical_target": conflict.CanonicalCode,
		}
	}
	return details
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
	if stable := stableDatabaseError(operation, err); stable != nil {
		return stable
	}
	return domain.WrapError(domain.Storage, "project_storage_failed", "could not access project registrations", map[string]any{"operation": operation}, err)
}
