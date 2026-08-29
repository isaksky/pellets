package app

import (
	"context"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type ProjectDatabaseOpener func(ctx context.Context, path string) (storage.ProjectDatabase, error)

type Database struct {
	Root string
	Path string
}

// ProjectDiscovery contains only read-only Git/filesystem operations. They
// run before storage opens a write transaction.
type ProjectDiscovery struct {
	FindGitIdentity func(ctx context.Context, workingDirectory string) (domain.GitIdentity, error)
	FindDatabase    func(workingDirectory string) (Database, error)
	NormalizePath   func(databaseRoot, localPath string) (domain.LocalPath, error)
	ResolvePath     func(databaseRoot string, stored domain.LocalPath) (string, error)
	PathExists      func(path string) (bool, error)
}

type ProjectDatabaseInitializer func(ctx context.Context, root string) (InitializedDatabase, error)

// ProjectManager registers logical Git repositories and their worktree
// workspaces, and resolves the current pair for downstream use cases.
type ProjectManager struct {
	Discover   ProjectDiscovery
	Initialize ProjectDatabaseInitializer
	Open       ProjectDatabaseOpener
	GitSafety  DatabaseGitSafety
}

func (manager ProjectManager) Init(ctx context.Context, workingDirectory, code string) (storage.Project, error) {
	if err := manager.validate(); err != nil {
		return storage.Project{}, err
	}
	if err := domain.ValidateProjectCode(code); err != nil {
		return storage.Project{}, err
	}
	identity, err := manager.Discover.FindGitIdentity(ctx, workingDirectory)
	if err != nil {
		return storage.Project{}, err
	}

	database, err := manager.Discover.FindDatabase(workingDirectory)
	if err != nil {
		if domain.PublicError(err).Code != "database_not_found" {
			return storage.Project{}, err
		}
		initialized, initializeErr := manager.Initialize(ctx, identity.WorkTreeRoot)
		if initializeErr != nil {
			return storage.Project{}, initializeErr
		}
		database = Database{Root: initialized.Root, Path: initialized.Path}
	}
	registration, err := manager.registration(database.Root, identity, code)
	if err != nil {
		return storage.Project{}, err
	}

	// Initialization is the only command allowed to update Git's local exclude
	// and register a workspace. Both safeguards finish before storage writes.
	if err := manager.GitSafety.RejectTracked(ctx, database.Root, database.Path); err != nil {
		return storage.Project{}, err
	}
	if err := manager.GitSafety.EnsureExcluded(ctx, database.Root, database.Path); err != nil {
		return storage.Project{}, err
	}

	projectDatabase, err := manager.Open(ctx, database.Path)
	if err != nil {
		return storage.Project{}, err
	}
	if existing, lookupErr := projectDatabase.FindWorkspaceByGitDir(ctx, registration.GitDir); lookupErr == nil {
		if existing.Workspace.RootPath != registration.WorkspaceRoot {
			oldRoot, resolveErr := manager.Discover.ResolvePath(database.Root, existing.Workspace.RootPath)
			if resolveErr != nil {
				return storage.Project{}, closeProjectDatabase(projectDatabase, resolveErr)
			}
			exists, existsErr := manager.Discover.PathExists(oldRoot)
			if existsErr != nil {
				return storage.Project{}, closeProjectDatabase(projectDatabase, existsErr)
			}
			registration.AllowWorkspaceMove = !exists
		}
	} else if domain.PublicError(lookupErr).Code != "workspace_not_registered" {
		return storage.Project{}, closeProjectDatabase(projectDatabase, lookupErr)
	}

	project, _, operationErr := projectDatabase.RegisterProject(ctx, registration)
	return project, closeProjectDatabase(projectDatabase, operationErr)
}

func (manager ProjectManager) registration(databaseRoot string, identity domain.GitIdentity, code string) (storage.ProjectRegistration, error) {
	commonDir, err := manager.Discover.NormalizePath(databaseRoot, identity.GitCommonDir)
	if err != nil {
		return storage.ProjectRegistration{}, err
	}
	workspaceRoot, err := manager.Discover.NormalizePath(databaseRoot, identity.WorkTreeRoot)
	if err != nil {
		return storage.ProjectRegistration{}, err
	}
	gitDir, err := manager.Discover.NormalizePath(databaseRoot, identity.GitDir)
	if err != nil {
		return storage.ProjectRegistration{}, err
	}
	return storage.ProjectRegistration{
		Code: code, GitCommonDir: commonDir, WorkspaceRoot: workspaceRoot, GitDir: gitDir,
	}, nil
}

func (manager ProjectManager) List(ctx context.Context, database Database) ([]storage.Project, error) {
	if err := manager.validateOpen(); err != nil {
		return nil, err
	}
	projectDatabase, err := manager.Open(ctx, database.Path)
	if err != nil {
		return nil, err
	}
	projects, operationErr := projectDatabase.ListProjects(ctx)
	return projects, closeProjectDatabase(projectDatabase, operationErr)
}

func (manager ProjectManager) ShowByCode(ctx context.Context, database Database, code string) (storage.Project, error) {
	if err := manager.validateOpen(); err != nil {
		return storage.Project{}, err
	}
	if err := domain.ValidateProjectCode(code); err != nil {
		return storage.Project{}, err
	}
	projectDatabase, err := manager.Open(ctx, database.Path)
	if err != nil {
		return storage.Project{}, err
	}
	project, operationErr := projectDatabase.FindProjectByCode(ctx, code)
	return project, closeProjectDatabase(projectDatabase, operationErr)
}

func (manager ProjectManager) ShowCurrent(ctx context.Context, database Database, workingDirectory string) (storage.Project, error) {
	resolved, err := manager.ResolveCurrent(ctx, database, workingDirectory)
	return resolved.Project, err
}

// ResolveCurrent is the foundation boundary used by queue and lifecycle
// services. It is read-only and never registers an unrecognized worktree.
func (manager ProjectManager) ResolveCurrent(ctx context.Context, database Database, workingDirectory string) (storage.ResolvedProject, error) {
	if err := manager.validateCurrent(); err != nil {
		return storage.ResolvedProject{}, err
	}
	identity, err := manager.Discover.FindGitIdentity(ctx, workingDirectory)
	if err != nil {
		return storage.ResolvedProject{}, err
	}
	registration, err := manager.registration(database.Root, identity, "unused")
	if err != nil {
		return storage.ResolvedProject{}, err
	}
	projectDatabase, err := manager.Open(ctx, database.Path)
	if err != nil {
		return storage.ResolvedProject{}, err
	}
	resolved, operationErr := projectDatabase.ResolveProjectWorkspace(
		ctx, registration.GitCommonDir, registration.WorkspaceRoot, registration.GitDir)
	return resolved, closeProjectDatabase(projectDatabase, operationErr)
}

// ResolveSelectedCurrentProject resolves the current registered worktree and
// validates that an optional explicit selection identifies its logical project.
// Project-scoped commands cannot use --project to cross repository boundaries.
func (manager ProjectManager) ResolveSelectedCurrentProject(
	ctx context.Context,
	database Database,
	workingDirectory string,
	selectedCode string,
) (storage.ResolvedProject, error) {
	if selectedCode != "" {
		if err := domain.ValidateProjectCode(selectedCode); err != nil {
			return storage.ResolvedProject{}, err
		}
	}
	resolved, err := manager.ResolveCurrent(ctx, database, workingDirectory)
	if err != nil {
		return storage.ResolvedProject{}, err
	}
	if selectedCode != "" && selectedCode != resolved.Project.Code {
		return storage.ResolvedProject{}, domain.NewError(
			domain.Usage,
			"project_selection_mismatch",
			"the selected project does not identify the current Git repository",
			map[string]any{"selected_project": selectedCode, "current_project": resolved.Project.Code},
		)
	}
	return resolved, nil
}

// ResolvePelletProject additionally validates public pellet references against
// the already selected current logical project.
func (manager ProjectManager) ResolvePelletProject(
	ctx context.Context,
	database Database,
	workingDirectory string,
	selectedCode string,
	references ...domain.PelletReference,
) (storage.ResolvedProject, error) {
	resolved, err := manager.ResolveSelectedCurrentProject(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return storage.ResolvedProject{}, err
	}
	for _, reference := range references {
		if reference.ProjectCode != resolved.Project.Code {
			return storage.ResolvedProject{}, domain.NewError(
				domain.Usage,
				"reference_project_mismatch",
				"the pellet reference belongs to a different logical project",
				map[string]any{
					"reference":         reference.String(),
					"reference_project": reference.ProjectCode,
					"current_project":   resolved.Project.Code,
				},
			)
		}
	}
	return resolved, nil
}

func (manager ProjectManager) validate() error {
	if manager.Discover.FindGitIdentity == nil || manager.Discover.FindDatabase == nil || manager.Discover.NormalizePath == nil || manager.Discover.ResolvePath == nil || manager.Discover.PathExists == nil || manager.Initialize == nil || manager.Open == nil || manager.GitSafety == nil {
		return projectManagerConfigurationError()
	}
	return nil
}

func (manager ProjectManager) validateOpen() error {
	if manager.Open == nil {
		return projectManagerConfigurationError()
	}
	return nil
}

func (manager ProjectManager) validateCurrent() error {
	if manager.Discover.FindGitIdentity == nil || manager.Discover.NormalizePath == nil || manager.Open == nil {
		return projectManagerConfigurationError()
	}
	return nil
}

func projectManagerConfigurationError() error {
	return domain.NewError(domain.Unexpected, "internal_error", "project manager is not configured", nil)
}

func closeProjectDatabase(database storage.ProjectDatabase, operationErr error) error {
	closeErr := database.Close()
	if operationErr != nil {
		if closeErr != nil {
			return errors.Join(operationErr, closeErr)
		}
		return operationErr
	}
	if closeErr != nil {
		return domain.WrapError(domain.Storage, "database_close_failed", "could not close the Pellets database", nil, closeErr)
	}
	return nil
}
