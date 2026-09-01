package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

// BootstrapCurrent ensures that the nearest database, logical Git repository,
// and current worktree workspace exist before a current-project command runs.
// An already registered exact workspace takes the read-only fast path.
func (manager ProjectManager) BootstrapCurrent(ctx context.Context, workingDirectory string) (Database, error) {
	if err := manager.validate(); err != nil {
		return Database{}, err
	}
	identity, err := manager.Discover.FindGitIdentity(ctx, workingDirectory)
	if err != nil {
		return Database{}, err
	}

	database, err := manager.Discover.FindDatabase(workingDirectory)
	if err != nil {
		if domain.PublicError(err).Code != "database_not_found" {
			return Database{}, err
		}
		initialized, initializeErr := manager.Initialize(ctx, identity.WorkTreeRoot)
		if initializeErr != nil {
			// Another valid first command may have won the exclusive database
			// creation race. Rediscovery lets both commands converge on it; every
			// other initialization failure remains authoritative.
			if domain.PublicError(initializeErr).Code != "database_already_exists" {
				return Database{}, initializeErr
			}
			database, err = manager.Discover.FindDatabase(workingDirectory)
			if err != nil {
				return Database{}, initializeErr
			}
		} else {
			database = Database{Root: initialized.Root, Path: initialized.Path}
		}
	}
	registration, err := manager.registration(database.Root, identity)
	if err != nil {
		return Database{}, err
	}

	projectDatabase, err := manager.openForBootstrap(ctx, database.Path)
	if err != nil {
		return Database{}, err
	}
	_, resolveErr := projectDatabase.ResolveProjectWorkspace(
		ctx, registration.GitCommonDir, registration.WorkspaceRoot, registration.GitDir)
	if resolveErr == nil {
		return database, closeProjectDatabase(projectDatabase, nil)
	}
	resolveCode := domain.PublicError(resolveErr).Code
	if resolveCode != "project_not_registered" && resolveCode != "workspace_not_registered" {
		return Database{}, closeProjectDatabase(projectDatabase, resolveErr)
	}
	if err := closeProjectDatabase(projectDatabase, nil); err != nil {
		return Database{}, err
	}

	// Git and filesystem safeguards finish before the registration write.
	if err := manager.GitSafety.RejectTracked(ctx, database.Root, database.Path); err != nil {
		return Database{}, err
	}
	if err := manager.GitSafety.EnsureExcluded(ctx, database.Root, database.Path); err != nil {
		return Database{}, err
	}

	projectDatabase, err = manager.openForBootstrap(ctx, database.Path)
	if err != nil {
		return Database{}, err
	}
	if existing, lookupErr := projectDatabase.FindWorkspaceByGitDir(ctx, registration.GitDir); lookupErr == nil {
		if existing.Workspace.RootPath != registration.WorkspaceRoot {
			oldRoot, resolveErr := manager.Discover.ResolvePath(database.Root, existing.Workspace.RootPath)
			if resolveErr != nil {
				return Database{}, closeProjectDatabase(projectDatabase, resolveErr)
			}
			exists, existsErr := manager.Discover.PathExists(oldRoot)
			if existsErr != nil {
				return Database{}, closeProjectDatabase(projectDatabase, existsErr)
			}
			registration.AllowWorkspaceMove = !exists
		}
	} else if domain.PublicError(lookupErr).Code != "workspace_not_registered" {
		return Database{}, closeProjectDatabase(projectDatabase, lookupErr)
	}

	_, _, operationErr := projectDatabase.RegisterProject(ctx, registration)
	return database, closeProjectDatabase(projectDatabase, operationErr)
}

func (manager ProjectManager) openForBootstrap(ctx context.Context, path string) (storage.ProjectDatabase, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		database, err := manager.Open(ctx, path)
		if err == nil {
			return database, nil
		}
		public := domain.PublicError(err)
		retryable := public.Code == "database_busy" || public.Code == "database_configuration_failed"
		if !retryable || time.Now().After(deadline) {
			return nil, err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (manager ProjectManager) registration(databaseRoot string, identity domain.GitIdentity) (storage.ProjectRegistration, error) {
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
		CodeName:     logicalRepositoryName(identity.GitCommonDir),
		CodeIdentity: fmt.Sprintf("%t:%s", commonDir.Relative, commonDir.Value),
		GenerateCode: true,
		GitCommonDir: commonDir, WorkspaceRoot: workspaceRoot, GitDir: gitDir,
	}, nil
}

func logicalRepositoryName(gitCommonDir string) string {
	clean := filepath.Clean(gitCommonDir)
	name := filepath.Base(clean)
	if strings.EqualFold(name, ".git") {
		return filepath.Base(filepath.Dir(clean))
	}
	if len(name) > len(".git") && strings.EqualFold(name[len(name)-len(".git"):], ".git") {
		name = name[:len(name)-len(".git")]
	}
	return name
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
	registration, err := manager.registration(database.Root, identity)
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
