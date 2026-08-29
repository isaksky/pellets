package app

import (
	"context"
	"errors"

	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

// ProjectDatabaseOpener opens the project storage boundary at path.
type ProjectDatabaseOpener func(ctx context.Context, path string) (storage.ProjectDatabase, error)

// ProjectManager registers and resolves Git work trees in selected databases.
type ProjectManager struct {
	Initialize DatabaseInitializer
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
	gitRoot, err := discovery.FindGitRoot(ctx, workingDirectory)
	if err != nil {
		return storage.Project{}, err
	}

	database, err := discovery.FindDatabase(workingDirectory)
	if err != nil {
		if domain.PublicError(err).Code != "database_not_found" {
			return storage.Project{}, err
		}
		initialized, initializeErr := manager.Initialize.Init(ctx, gitRoot)
		if initializeErr != nil {
			return storage.Project{}, initializeErr
		}
		database = discovery.Database{Root: initialized.Root, Path: initialized.Path}
	}

	rootPath, err := discovery.RelativeProjectPath(database.Root, gitRoot)
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
func (manager ProjectManager) List(ctx context.Context, database discovery.Database) ([]storage.Project, error) {
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
func (manager ProjectManager) ShowByCode(ctx context.Context, database discovery.Database, code string) (storage.Project, error) {
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
func (manager ProjectManager) ShowCurrent(ctx context.Context, database discovery.Database, workingDirectory string) (storage.Project, error) {
	if err := manager.validateOpen(); err != nil {
		return storage.Project{}, err
	}
	gitRoot, err := discovery.FindGitRoot(ctx, workingDirectory)
	if err != nil {
		return storage.Project{}, err
	}
	rootPath, err := discovery.RelativeProjectPath(database.Root, gitRoot)
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
	if manager.Initialize.Open == nil || manager.Initialize.GitSafety == nil || manager.Open == nil || manager.GitSafety == nil {
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
