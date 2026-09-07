package discovery

import (
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
)

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
