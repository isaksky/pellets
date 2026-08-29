package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
)

func TestInitRemovesNewDatabaseWhenGitSafetyCannotBeCompleted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	initializer := DatabaseInitializer{
		Open: func(_ context.Context, path string) (DatabaseHandle, error) {
			file, err := os.Open(path)
			return file, err
		},
		GitSafety: failingExcludeSafety{},
	}

	if _, err := initializer.Init(context.Background(), root); err == nil {
		t.Fatal("Init unexpectedly succeeded")
	}
	if _, err := os.Stat(discovery.DatabasePath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database after failed Git safeguard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, discovery.MetadataDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new metadata directory after failed Git safeguard: %v", err)
	}
}

type failingExcludeSafety struct{}

func (failingExcludeSafety) RejectTracked(context.Context, string, string) error { return nil }

func (failingExcludeSafety) EnsureExcluded(context.Context, string, string) error {
	return errors.New("exclude unavailable")
}
