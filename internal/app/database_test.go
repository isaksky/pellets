package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
	"pellets/internal/domain"
)

func TestInitRemovesNewDatabaseWhenGitSafetyCannotBeCompleted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	initializer := DatabaseInitializer{
		Open: func(_ context.Context, path string) (DatabaseHandle, error) {
			if err := os.WriteFile(path+"-wal", []byte("created by initializer"), 0o600); err != nil {
				return nil, err
			}
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
	if _, err := os.Stat(discovery.DatabasePath(root) + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned WAL after failed Git safeguard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, discovery.MetadataDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new metadata directory after failed Git safeguard: %v", err)
	}
}

func TestInitRejectsPreexistingCompanionsWithoutCallingDependencies(t *testing.T) {
	t.Parallel()

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		suffix := suffix
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			databasePath := discovery.DatabasePath(root)
			if err := os.Mkdir(filepath.Dir(databasePath), 0o755); err != nil {
				t.Fatal(err)
			}
			companionPath := databasePath + suffix
			sentinel := []byte("preexisting " + suffix)
			if err := os.WriteFile(companionPath, sentinel, 0o600); err != nil {
				t.Fatal(err)
			}
			dependencies := &countingInitializationDependencies{}
			initializer := DatabaseInitializer{Open: dependencies.open, GitSafety: dependencies}

			_, err := initializer.Init(context.Background(), root)
			if err == nil || publicCode(err) != "database_companion_already_exists" {
				t.Fatalf("Init error = %v", err)
			}
			if dependencies.calls != 1 {
				t.Fatalf("dependency calls = %d, want only the read-only Git preflight", dependencies.calls)
			}
			got, readErr := os.ReadFile(companionPath)
			if readErr != nil || string(got) != string(sentinel) {
				t.Fatalf("companion after rejection = %q, %v", got, readErr)
			}
		})
	}
}

func TestInitCleanupLeavesAReplacedCompanion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	databasePath := discovery.DatabasePath(root)
	replacement := []byte("replacement must survive")
	initializer := DatabaseInitializer{
		Open: func(_ context.Context, path string) (DatabaseHandle, error) {
			companionPath := path + "-wal"
			if err := os.WriteFile(companionPath, []byte("owned WAL"), 0o600); err != nil {
				return nil, err
			}
			return replacingCompanionHandle{path: companionPath, replacement: replacement}, nil
		},
		GitSafety: noOpInitializationSafety{},
	}

	_, err := initializer.Init(context.Background(), root)
	if err == nil || publicCode(err) != "database_cleanup_failed" {
		t.Fatalf("Init error = %v", err)
	}
	got, readErr := os.ReadFile(databasePath + "-wal")
	if readErr != nil || string(got) != string(replacement) {
		t.Fatalf("replacement after cleanup = %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(databasePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("owned database after cleanup: %v", statErr)
	}
}

type failingExcludeSafety struct{}

func (failingExcludeSafety) RejectTracked(context.Context, string, string) error { return nil }

func (failingExcludeSafety) EnsureExcluded(context.Context, string, string) error {
	return errors.New("exclude unavailable")
}

type countingInitializationDependencies struct {
	calls int
}

func (dependencies *countingInitializationDependencies) open(context.Context, string) (DatabaseHandle, error) {
	dependencies.calls++
	return nil, errors.New("unexpected database open")
}

func (dependencies *countingInitializationDependencies) RejectTracked(context.Context, string, string) error {
	dependencies.calls++
	return nil
}

func (dependencies *countingInitializationDependencies) EnsureExcluded(context.Context, string, string) error {
	dependencies.calls++
	return nil
}

type noOpInitializationSafety struct{}

func (noOpInitializationSafety) RejectTracked(context.Context, string, string) error  { return nil }
func (noOpInitializationSafety) EnsureExcluded(context.Context, string, string) error { return nil }

type replacingCompanionHandle struct {
	path        string
	replacement []byte
}

func (handle replacingCompanionHandle) Close() error {
	if err := os.Remove(handle.path); err != nil {
		return err
	}
	if err := os.WriteFile(handle.path, handle.replacement, 0o600); err != nil {
		return err
	}
	return errors.New("close failed after replacing WAL")
}

func publicCode(err error) string {
	return domain.PublicError(err).Code
}
