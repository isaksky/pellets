// Package app owns Pellets use-case sequencing and transaction boundaries.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"pellets/internal/discovery"
	"pellets/internal/domain"
)

// DatabaseHandle is the lifetime boundary required during initialization.
type DatabaseHandle interface {
	Close() error
}

// DatabaseOpener creates/configures/migrates the database at path.
type DatabaseOpener func(ctx context.Context, path string) (DatabaseHandle, error)

// DatabaseGitSafety is the local-only Git safety boundary used by database
// initialization.
type DatabaseGitSafety interface {
	RejectTracked(ctx context.Context, databaseRoot, databasePath string) error
	EnsureExcluded(ctx context.Context, databaseRoot, databasePath string) error
}

// DatabaseInitializer creates a new database without ever opening an existing
// path as part of initialization.
type DatabaseInitializer struct {
	Open      DatabaseOpener
	GitSafety DatabaseGitSafety
}

// InitializedDatabase describes a successfully created database.
type InitializedDatabase struct {
	Root string
	Path string
}

// Init creates and migrates the fixed database path beneath root. The empty
// file is claimed with O_EXCL before SQLite opens it, preventing overwrite races.
func (initializer DatabaseInitializer) Init(ctx context.Context, root string) (InitializedDatabase, error) {
	if initializer.Open == nil || initializer.GitSafety == nil {
		return InitializedDatabase{}, domain.NewError(
			domain.Unexpected,
			"internal_error",
			"database initializer is not configured",
			nil,
		)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return InitializedDatabase{}, databaseCreationFailure(root, err)
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return InitializedDatabase{}, databaseCreationFailure(absoluteRoot, err)
	}
	if !info.IsDir() {
		return InitializedDatabase{}, databaseCreationFailure(absoluteRoot, errors.New("database root is not a directory"))
	}

	databasePath := discovery.DatabasePath(absoluteRoot)
	if err := initializer.GitSafety.RejectTracked(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, err
	}

	metadataPath := filepath.Dir(databasePath)
	metadataCreated := false
	if err := os.Mkdir(metadataPath, 0o777); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return InitializedDatabase{}, databaseCreationFailure(databasePath, err)
		}
	} else {
		metadataCreated = true
	}

	file, err := os.OpenFile(databasePath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return InitializedDatabase{}, domain.NewError(
				domain.Conflict,
				"database_already_exists",
				"a Pellets database already exists in the current directory",
				map[string]any{"database_path": databasePath},
			)
		}
		if metadataCreated {
			_ = os.Remove(metadataPath)
		}
		return InitializedDatabase{}, databaseCreationFailure(databasePath, err)
	}
	if err := file.Close(); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, metadataPath, metadataCreated, err)
	}

	database, err := initializer.Open(ctx, databasePath)
	if err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, metadataPath, metadataCreated, err)
	}
	if err := database.Close(); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, metadataPath, metadataCreated, err)
	}
	// Re-check after SQLite closes the new database so a concurrent index
	// update cannot turn initialization into a successful tracked database.
	if err := initializer.GitSafety.RejectTracked(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, metadataPath, metadataCreated, err)
	}
	if err := initializer.GitSafety.EnsureExcluded(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, metadataPath, metadataCreated, err)
	}

	return InitializedDatabase{Root: absoluteRoot, Path: databasePath}, nil
}

func cleanupFailedInitialization(databasePath, metadataPath string, metadataCreated bool, cause error) error {
	var cleanupErrors []error
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm", databasePath + "-journal"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	if metadataCreated {
		if err := os.Remove(metadataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove %s: %w", metadataPath, err))
		}
	}
	if len(cleanupErrors) == 0 {
		return cause
	}
	cleanupErrors = append([]error{cause}, cleanupErrors...)
	return domain.WrapError(
		domain.Storage,
		"database_cleanup_failed",
		"database initialization failed and its new files could not be completely removed",
		map[string]any{"database_path": databasePath},
		errors.Join(cleanupErrors...),
	)
}

func databaseCreationFailure(path string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"database_creation_failed",
		"could not create the Pellets database",
		map[string]any{"database_path": path},
		err,
	)
}
