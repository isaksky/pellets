// Package app owns Pellets use-case sequencing and transaction boundaries.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"pellets/internal/domain"
)

// DatabaseHandle is the lifetime boundary required during initialization.
type DatabaseHandle interface {
	Close() error
}

// DatabaseOpener creates/configures/migrates the database at path.
type DatabaseOpener func(ctx context.Context, path string) (DatabaseHandle, error)

// DatabasePath locates the fixed database path beneath a selected root.
// The composition root supplies the product's concrete filesystem layout.
type DatabasePath func(root string) string

// DatabaseGitSafety is the local-only Git safety boundary used by database
// initialization.
type DatabaseGitSafety interface {
	RejectTracked(ctx context.Context, databaseRoot, databasePath string) error
	EnsureExcluded(ctx context.Context, databaseRoot, databasePath string) error
}

// DatabaseInitializer creates a new database without ever opening an existing
// path as part of initialization.
type DatabaseInitializer struct {
	Path      DatabasePath
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
	if err := initializer.validate(); err != nil {
		return InitializedDatabase{}, err
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

	databasePath := initializer.Path(absoluteRoot)
	metadataPath := filepath.Dir(databasePath)
	metadataInfo, metadataExists, err := inspectMetadataDirectory(metadataPath)
	if err != nil {
		return InitializedDatabase{}, err
	}
	if err := initializer.GitSafety.RejectTracked(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, err
	}
	if err := rejectExistingDatabasePaths(databasePath); err != nil {
		return InitializedDatabase{}, err
	}

	owned := initializationOwnership{files: make(map[string]os.FileInfo)}
	if err := os.Mkdir(metadataPath, 0o777); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return InitializedDatabase{}, databaseCreationFailure(databasePath, err)
		}
	} else {
		owned.metadata, _, err = inspectMetadataDirectory(metadataPath)
		if err != nil {
			return InitializedDatabase{}, err
		}
	}
	if metadataExists {
		if err := verifySameDirectory(metadataPath, metadataInfo); err != nil {
			return InitializedDatabase{}, err
		}
	} else if owned.metadata == nil {
		// Mkdir reported that a path appeared after the first inspection. It must
		// still be a real directory, but it is not owned by this attempt.
		if _, _, err := inspectMetadataDirectory(metadataPath); err != nil {
			return InitializedDatabase{}, err
		}
	}
	// Repeat the no-overwrite preflight after creating/revalidating .pellets so
	// a path that appeared while Git was inspected is never opened or removed.
	if err := rejectExistingDatabasePaths(databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
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
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, databaseCreationFailure(databasePath, err))
	}
	databaseInfo, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, statErr)
	}
	owned.files[databasePath] = databaseInfo
	if err := file.Close(); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
	}

	database, openErr := initializer.Open(ctx, databasePath)
	if captureErr := owned.captureNewCompanions(databasePath); captureErr != nil {
		if database != nil {
			captureErr = errors.Join(captureErr, database.Close())
		}
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, errors.Join(openErr, captureErr))
	}
	if openErr != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, openErr)
	}
	if database == nil {
		return InitializedDatabase{}, cleanupFailedInitialization(
			databasePath,
			owned,
			domain.NewError(domain.Unexpected, "internal_error", "database opener returned no handle", nil),
		)
	}
	if err := database.Close(); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
	}
	if err := owned.captureNewCompanions(databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
	}
	// Re-check after SQLite closes the new database so a concurrent index
	// update cannot turn initialization into a successful tracked database.
	if err := initializer.GitSafety.RejectTracked(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
	}
	if err := initializer.GitSafety.EnsureExcluded(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
	}
	// Verify the completed database remains absent from the index after the
	// exclude update. This is the final successful-initialization gate.
	if err := initializer.GitSafety.RejectTracked(ctx, absoluteRoot, databasePath); err != nil {
		return InitializedDatabase{}, cleanupFailedInitialization(databasePath, owned, err)
	}

	return InitializedDatabase{Root: absoluteRoot, Path: databasePath}, nil
}

type initializationOwnership struct {
	files    map[string]os.FileInfo
	metadata os.FileInfo
}

func (owned *initializationOwnership) captureNewCompanions(databasePath string) error {
	for _, path := range databasePaths(databasePath)[1:] {
		if _, exists := owned.files[path]; exists {
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return databaseCreationFailure(path, err)
		}
		if !info.Mode().IsRegular() {
			return databaseCompanionAlreadyExists(databasePath, path)
		}
		file, err := os.Open(path)
		if err != nil {
			return databaseCreationFailure(path, err)
		}
		stableInfo, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil || closeErr != nil {
			return databaseCreationFailure(path, errors.Join(statErr, closeErr))
		}
		if !stableInfo.Mode().IsRegular() {
			return databaseCompanionAlreadyExists(databasePath, path)
		}
		// Every companion was proven absent immediately before Open. Record its
		// handle-backed identity now so cleanup can remove this file, but never a
		// replacement. Windows Lstat identities are otherwise resolved lazily by
		// SameFile and can accidentally resolve to a replacement at cleanup time.
		owned.files[path] = stableInfo
	}
	return nil
}

func cleanupFailedInitialization(databasePath string, owned initializationOwnership, cause error) error {
	var cleanupErrors []error
	paths := databasePaths(databasePath)
	for index := len(paths) - 1; index >= 0; index-- {
		path := paths[index]
		original, ok := owned.files[path]
		if !ok {
			continue
		}
		if err := removeOwnedPath(path, original); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if owned.metadata != nil {
		metadataPath := filepath.Dir(databasePath)
		if err := removeOwnedPath(metadataPath, owned.metadata); err != nil {
			cleanupErrors = append(cleanupErrors, err)
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

func removeOwnedPath(path string, original os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect owned path %s: %w", path, err)
	}
	if !os.SameFile(original, current) {
		return fmt.Errorf("refuse to remove replaced path %s", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

func inspectMetadataDirectory(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, databaseCreationFailure(path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, true, domain.NewError(
			domain.Conflict,
			"database_metadata_symlink",
			"the Pellets metadata directory must not be a symbolic link",
			map[string]any{"metadata_path": path},
		)
	}
	if !info.IsDir() {
		return nil, true, databaseCreationFailure(path, errors.New("metadata path is not a directory"))
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, true, databaseCreationFailure(path, err)
	}
	stableInfo, statErr := directory.Stat()
	closeErr := directory.Close()
	if statErr != nil || closeErr != nil {
		return nil, true, databaseCreationFailure(path, errors.Join(statErr, closeErr))
	}
	if !stableInfo.IsDir() {
		return nil, true, databaseCreationFailure(path, errors.New("metadata path changed during inspection"))
	}
	return stableInfo, true, nil
}

func verifySameDirectory(path string, original os.FileInfo) error {
	current, _, err := inspectMetadataDirectory(path)
	if err != nil {
		return err
	}
	if current == nil || !os.SameFile(original, current) {
		return databaseCreationFailure(path, errors.New("metadata directory changed during initialization"))
	}
	return nil
}

func rejectExistingDatabasePaths(databasePath string) error {
	for index, path := range databasePaths(databasePath) {
		_, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return databaseCreationFailure(path, err)
		}
		if index == 0 {
			return databaseAlreadyExists(databasePath)
		}
		return databaseCompanionAlreadyExists(databasePath, path)
	}
	return nil
}

func (initializer DatabaseInitializer) validate() error {
	if initializer.Path == nil || initializer.Open == nil || initializer.GitSafety == nil {
		return domain.NewError(
			domain.Unexpected,
			"internal_error",
			"database initializer is not configured",
			nil,
		)
	}
	return nil
}

func databasePaths(databasePath string) []string {
	return []string{
		databasePath,
		databasePath + "-wal",
		databasePath + "-shm",
		databasePath + "-journal",
	}
}

func databaseAlreadyExists(databasePath string) error {
	return domain.NewError(
		domain.Conflict,
		"database_already_exists",
		"a Pellets database already exists in the current directory",
		map[string]any{"database_path": databasePath},
	)
}

func databaseCompanionAlreadyExists(databasePath, companionPath string) error {
	return domain.NewError(
		domain.Conflict,
		"database_companion_already_exists",
		"a SQLite companion path already exists for the Pellets database",
		map[string]any{"companion_path": companionPath, "database_path": databasePath},
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
