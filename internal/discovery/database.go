// Package discovery locates Pellets databases and Git work trees.
package discovery

import (
	"errors"
	"os"
	"path/filepath"

	"pellets/internal/domain"
)

const (
	// MetadataDirectory is the directory Pellets owns beneath a database root.
	MetadataDirectory = ".pellets"
	// DatabaseFilename is the fixed SQLite database filename.
	DatabaseFilename = "pellets.db"
)

// Database identifies a discovered database and the root that contains its
// .pellets directory.
type Database struct {
	Root string
	Path string
}

// DatabasePath returns the one supported database path beneath root.
func DatabasePath(root string) string {
	return filepath.Join(root, MetadataDirectory, DatabaseFilename)
}

// FindDatabase walks from start to the filesystem root and returns the nearest
// .pellets/pellets.db. Git repository boundaries do not affect the walk.
func FindDatabase(start string) (Database, error) {
	absolute, err := filepath.Abs(start)
	if err != nil {
		return Database{}, discoveryFailure(start, err)
	}
	absolute = filepath.Clean(absolute)

	info, err := os.Stat(absolute)
	if err != nil {
		return Database{}, discoveryFailure(absolute, err)
	}
	if !info.IsDir() {
		return Database{}, discoveryFailure(absolute, errors.New("starting path is not a directory"))
	}

	for root := absolute; ; root = filepath.Dir(root) {
		path := DatabasePath(root)
		if _, err := os.Lstat(path); err == nil {
			return Database{Root: root, Path: path}, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return Database{}, discoveryFailure(path, err)
		}

		parent := filepath.Dir(root)
		if parent == root {
			break
		}
	}

	return Database{}, domain.NewError(
		domain.NotFound,
		"database_not_found",
		"no Pellets database was found in the current directory or its ancestors",
		map[string]any{"start_path": absolute},
	)
}

func discoveryFailure(path string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"database_discovery_failed",
		"could not search for a Pellets database",
		map[string]any{"path": path},
		err,
	)
}
