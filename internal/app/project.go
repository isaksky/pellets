package app

import (
	"context"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// ProjectDatabaseOpener opens the project storage boundary at path.
type ProjectDatabaseOpener func(ctx context.Context, path string) (storage.ProjectDatabase, error)

// Database identifies the selected database without exposing the concrete
// filesystem discovery package to application use cases.
type Database struct {
	Root string
	Path string
}

// ProjectDiscovery is the narrow discovery boundary needed by project use
// cases. cmd/pl supplies the concrete filesystem and Git functions.
type ProjectDiscovery struct {
	FindGitRoot         func(ctx context.Context, workingDirectory string) (string, error)
	FindDatabase        func(workingDirectory string) (Database, error)
	RelativeProjectPath func(databaseRoot, projectRoot string) (string, error)
}

// ProjectDatabaseInitializer initializes the database selected for a Git root.
type ProjectDatabaseInitializer func(ctx context.Context, root string) (InitializedDatabase, error)

// ProjectManager registers and resolves Git work trees in selected databases.
type ProjectManager struct {
	Discover   ProjectDiscovery
	Initialize ProjectDatabaseInitializer
	Open       ProjectDatabaseOpener
	GitSafety  DatabaseGitSafety
}

// Init registers the current Git work tree. With no ancestor database, it
// first creates one at the Git root.
func (manager ProjectManager) Init(ctx context.Context, workingDirectory, code string) (storage.Project, error) {
	if err := manager.validate(); err != nil {
		return storage.Project{}, err
	}
	if err := domain.ValidateProjectCode(code); err != nil {
		return storage.Project{}, err
	}
	gitRoot, err := manager.Discover.FindGitRoot(ctx, workingDirectory)
	if err != nil {
		return storage.Project{}, err
	}

	database, err := manager.Discover.FindDatabase(workingDirectory)
	if err != nil {
		if domain.PublicError(err).Code != "database_not_found" {
			return storage.Project{}, err
		}
		initialized, initializeErr := manager.Initialize(ctx, gitRoot)
		if initializeErr != nil {
			return storage.Project{}, initializeErr
		}
		database = Database{Root: initialized.Root, Path: initialized.Path}
	}

	rootPath, err := manager.Discover.RelativeProjectPath(database.Root, gitRoot)
	if err != nil {
		return storage.Project{}, err
	}
	// This also covers an existing database. Perform both checks before
	// registration so a failed safeguard cannot leave a new project row.
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
	project, _, operationErr := projectDatabase.RegisterProject(ctx, code, rootPath)
	return project, closeProjectDatabase(projectDatabase, operationErr)
}

// List returns every project registered in the selected database.
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

// ShowByCode returns a named project without requiring a current Git work tree.
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

// ShowCurrent resolves the nearest Git root to its registered relative path.
func (manager ProjectManager) ShowCurrent(ctx context.Context, database Database, workingDirectory string) (storage.Project, error) {
	if err := manager.validateCurrent(); err != nil {
		return storage.Project{}, err
	}
	gitRoot, err := manager.Discover.FindGitRoot(ctx, workingDirectory)
	if err != nil {
		return storage.Project{}, err
	}
	rootPath, err := manager.Discover.RelativeProjectPath(database.Root, gitRoot)
	if err != nil {
		return storage.Project{}, err
	}
	projectDatabase, err := manager.Open(ctx, database.Path)
	if err != nil {
		return storage.Project{}, err
	}
	project, operationErr := projectDatabase.FindProjectByRootPath(ctx, rootPath)
	return project, closeProjectDatabase(projectDatabase, operationErr)
}

func (manager ProjectManager) validate() error {
	if manager.Discover.FindGitRoot == nil || manager.Discover.FindDatabase == nil || manager.Discover.RelativeProjectPath == nil || manager.Initialize == nil || manager.Open == nil || manager.GitSafety == nil {
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
	if manager.Discover.FindGitRoot == nil || manager.Discover.RelativeProjectPath == nil || manager.Open == nil {
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
		return domain.WrapError(
			domain.Storage,
			"database_close_failed",
			"could not close the Pellets database",
			nil,
			closeErr,
		)
	}
	return nil
}
