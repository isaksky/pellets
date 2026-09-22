package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
)

func TestBindingPublishedDuringLockAcquisition(t *testing.T) {
	for _, test := range []struct {
		name      string
		lockError error
		content   string
		wantCode  string
	}{
		{name: "published before lock removal", lockError: os.ErrExist, content: `{"version":1,"path":"../.pellets/pellets.db"}`},
		{name: "published while lock deletion is pending", lockError: os.ErrPermission, content: `{"version":1,"path":"../.pellets/pellets.db"}`},
		{name: "permission failure without binding", lockError: os.ErrPermission, wantCode: "database_binding_failed"},
		{name: "invalid binding still fails", lockError: os.ErrPermission, content: "{", wantCode: "database_binding_failed"},
		{name: "unavailable binding still fails", lockError: os.ErrPermission, content: `{"version":1,"path":"../missing/.pellets/pellets.db"}`, wantCode: "database_binding_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			common := filepath.Join(root, ".git")
			for _, directory := range []string{common, filepath.Dir(DatabasePath(root))} {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(DatabasePath(root), []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			bootstrapCalls := 0
			got, err := withDatabaseBinding(context.Background(), common, func() (Database, error) {
				bootstrapCalls++
				database, _, err := readDatabaseBinding(common)
				return database, err
			}, func(path string, _ os.FileMode) error {
				if path != filepath.Join(common, databaseBindingName+".lock") {
					t.Fatalf("lock path = %q", path)
				}
				// Simulate the other process publishing after the initial read
				// but before this process's lock attempt returns.
				if test.content != "" {
					if err := os.WriteFile(filepath.Join(common, databaseBindingName), []byte(test.content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return &os.PathError{Op: "mkdir", Path: path, Err: test.lockError}
			})
			if test.wantCode != "" {
				if err == nil || domain.PublicError(err).Code != test.wantCode || bootstrapCalls != 0 {
					t.Fatalf("error = %v, bootstrap calls = %d", err, bootstrapCalls)
				}
				if test.content == "" && !errors.Is(err, test.lockError) {
					t.Fatalf("lost lock failure cause: %v", err)
				}
				return
			}
			if err != nil || got.Path != DatabasePath(root) || bootstrapCalls != 1 {
				t.Fatalf("database = %#v, error = %v, bootstrap calls = %d", got, err, bootstrapCalls)
			}
		})
	}
}

func TestRejectInvalidDatabaseBindings(t *testing.T) {
	for _, content := range []string{"", "{", `{"version":2,"path":"../.pellets/pellets.db"}`, `{"version":1,"path":""}`, `{"version":1,"path":"../wrong.db"}`} {
		t.Run(content, func(t *testing.T) {
			common := t.TempDir()
			if err := os.WriteFile(filepath.Join(common, databaseBindingName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, found, err := readDatabaseBinding(common)
			if !found || domain.PublicError(err).Code != "database_binding_failed" {
				t.Fatalf("found=%v error=%v", found, err)
			}
		})
	}
}

func TestRelativeDatabaseBindingSurvivesDirectoryMove(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "before")
	common := filepath.Join(root, ".git")
	if err := os.MkdirAll(common, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(DatabasePath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(DatabasePath(root), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, databaseBindingName), []byte(`{"version":1,"path":"../.pellets/pellets.db"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "after")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	database, found, err := readDatabaseBinding(filepath.Join(moved, ".git"))
	if err != nil || !found || database.Path != DatabasePath(moved) {
		t.Fatalf("database=%#v found=%v error=%v", database, found, err)
	}
}
