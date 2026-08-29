package app

import (
	"context"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type PelletRepositoryOpener func(ctx context.Context, path string) (storage.PelletRepository, error)

// PelletManager executes queue use cases after resolving the current logical
// project and its registered worktree workspace.
type PelletManager struct {
	Projects ProjectManager
	Open     PelletRepositoryOpener
}

func (manager PelletManager) Add(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	input storage.NewPellet,
) (storage.Pellet, error) {
	var references []domain.PelletReference
	if input.Placement != nil {
		references = append(references, input.Placement.Target)
	}
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode, references...)
	if err != nil {
		return storage.Pellet{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Pellet{}, err
	}
	pellet, operationErr := repository.CreatePellet(ctx, resolved, input)
	return pellet, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) Move(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	reference domain.PelletReference,
	placement storage.PelletPlacement,
) (storage.Pellet, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode, reference, placement.Target)
	if err != nil {
		return storage.Pellet{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Pellet{}, err
	}
	pellet, operationErr := repository.MovePellet(ctx, resolved, reference, placement)
	return pellet, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) List(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	options storage.PelletListOptions,
) ([]storage.Pellet, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return nil, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return nil, err
	}
	pellets, operationErr := repository.ListPellets(ctx, resolved, options)
	return pellets, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) Search(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	options storage.PelletSearchOptions,
) ([]storage.Pellet, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return nil, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return nil, err
	}
	pellets, operationErr := repository.SearchPellets(ctx, resolved, options)
	return pellets, closePelletRepository(repository, operationErr)
}

// Purge removes or previews closed pellets for an explicitly selected logical
// project. It deliberately resolves by code without requiring a current Git
// repository or registered workspace because purge is a database-level
// administrative operation.
func (manager PelletManager) Purge(
	ctx context.Context,
	database Database,
	selectedCode string,
	options storage.PelletPurgeOptions,
	dryRun bool,
) ([]domain.PelletReference, error) {
	if selectedCode == "" {
		return nil, domain.NewError(
			domain.Usage, "missing_required_flag", "purge requires an explicit --project",
			map[string]any{"flag": "--project"},
		)
	}
	if manager.Open == nil {
		return nil, pelletManagerConfigurationError()
	}
	project, err := manager.Projects.ShowByCode(ctx, database, selectedCode)
	if err != nil {
		return nil, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return nil, err
	}
	var references []domain.PelletReference
	if dryRun {
		references, err = repository.PreviewClosedPelletPurge(ctx, project, options)
	} else {
		references, err = repository.PurgeClosedPellets(ctx, project, options)
	}
	return references, closePelletRepository(repository, err)
}

func (manager PelletManager) Show(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	reference domain.PelletReference,
) (storage.Pellet, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode, reference)
	if err != nil {
		return storage.Pellet{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Pellet{}, err
	}
	pellet, operationErr := repository.ReadPellet(ctx, resolved, reference)
	return pellet, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) Edit(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	reference domain.PelletReference,
	changes storage.PelletChanges,
) (storage.Pellet, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode, reference)
	if err != nil {
		return storage.Pellet{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Pellet{}, err
	}
	pellet, operationErr := repository.UpdatePellet(ctx, resolved, reference, changes)
	return pellet, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) Next(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	externalID, group *string,
) (storage.NextSelection, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return storage.NextSelection{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.NextSelection{}, err
	}
	selection, operationErr := repository.NextPellet(ctx, resolved, externalID, group)
	return selection, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) StartNext(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	externalID, group *string,
) (storage.NextSelection, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return storage.NextSelection{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.NextSelection{}, err
	}
	selection, operationErr := repository.StartNextPellet(ctx, resolved, externalID, group)
	return selection, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) Transition(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	reference domain.PelletReference,
	request storage.PelletLifecycleRequest,
) (storage.PelletLifecycleResult, error) {
	resolved, err := manager.resolve(ctx, database, workingDirectory, selectedCode, reference)
	if err != nil {
		return storage.PelletLifecycleResult{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.PelletLifecycleResult{}, err
	}
	result, operationErr := repository.TransitionPellet(ctx, resolved, reference, request)
	return result, closePelletRepository(repository, operationErr)
}

func (manager PelletManager) resolve(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	references ...domain.PelletReference,
) (storage.ResolvedProject, error) {
	if manager.Open == nil {
		return storage.ResolvedProject{}, pelletManagerConfigurationError()
	}
	return manager.Projects.ResolvePelletProject(ctx, database, workingDirectory, selectedCode, references...)
}

func (manager PelletManager) open(ctx context.Context, path string) (storage.PelletRepository, error) {
	if manager.Open == nil {
		return nil, pelletManagerConfigurationError()
	}
	return manager.Open(ctx, path)
}

func closePelletRepository(repository storage.PelletRepository, operationErr error) error {
	closeErr := repository.Close()
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

func pelletManagerConfigurationError() error {
	return domain.NewError(domain.Unexpected, "internal_error", "pellet manager is not configured", nil)
}
