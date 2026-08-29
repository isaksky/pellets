package app

import (
	"context"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type MemoryRepositoryOpener func(ctx context.Context, path string) (storage.MemoryRepository, error)

// MemoryManager executes project-memory use cases after resolving the current
// registered worktree to its shared logical project.
type MemoryManager struct {
	Projects ProjectManager
	Open     MemoryRepositoryOpener
}

func (manager MemoryManager) Add(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	input storage.NewMemory,
) (storage.Memory, error) {
	project, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return storage.Memory{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Memory{}, err
	}
	memory, operationErr := repository.CreateMemory(ctx, project, input)
	return memory, closeMemoryRepository(repository, operationErr)
}

func (manager MemoryManager) List(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	options storage.MemoryListOptions,
) ([]storage.Memory, error) {
	project, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return nil, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return nil, err
	}
	memories, operationErr := repository.ListMemories(ctx, project, options)
	return memories, closeMemoryRepository(repository, operationErr)
}

func (manager MemoryManager) Show(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	memoryID int64,
) (storage.Memory, error) {
	project, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return storage.Memory{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Memory{}, err
	}
	memory, operationErr := repository.ReadMemory(ctx, project, memoryID)
	return memory, closeMemoryRepository(repository, operationErr)
}

func (manager MemoryManager) Approve(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
	memoryID int64,
) (storage.Memory, error) {
	project, err := manager.resolve(ctx, database, workingDirectory, selectedCode)
	if err != nil {
		return storage.Memory{}, err
	}
	repository, err := manager.open(ctx, database.Path)
	if err != nil {
		return storage.Memory{}, err
	}
	memory, operationErr := repository.ApproveMemory(ctx, project, memoryID)
	return memory, closeMemoryRepository(repository, operationErr)
}

func (manager MemoryManager) resolve(
	ctx context.Context,
	database Database,
	workingDirectory, selectedCode string,
) (storage.Project, error) {
	if manager.Open == nil {
		return storage.Project{}, memoryManagerConfigurationError()
	}
	resolved, err := manager.Projects.ResolveSelectedCurrentProject(ctx, database, workingDirectory, selectedCode)
	return resolved.Project, err
}

func (manager MemoryManager) open(ctx context.Context, path string) (storage.MemoryRepository, error) {
	if manager.Open == nil {
		return nil, memoryManagerConfigurationError()
	}
	return manager.Open(ctx, path)
}

func closeMemoryRepository(repository storage.MemoryRepository, operationErr error) error {
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

func memoryManagerConfigurationError() error {
	return domain.NewError(domain.Unexpected, "internal_error", "memory manager is not configured", nil)
}
