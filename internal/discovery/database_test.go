package discovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
)

func TestFindDatabaseUsesNearestAncestorAndCrossesGitBoundary(t *testing.T) {
	t.Parallel()

	outer := filepath.Join(t.TempDir(), "common parent with spaces")
	repository := filepath.Join(outer, "repository")
	deep := filepath.Join(repository, "one", "two")
	for _, directory := range []string{deep, filepath.Join(repository, ".git"), filepath.Join(outer, MetadataDirectory)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	outerDatabase := DatabasePath(outer)
	if err := os.WriteFile(outerDatabase, []byte("outer"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err := FindDatabase(deep)
	if err != nil {
		t.Fatal(err)
	}
	if database.Root != outer || database.Path != outerDatabase {
		t.Fatalf("FindDatabase before nesting = %#v, want root %q and path %q", database, outer, outerDatabase)
	}

	innerRoot := filepath.Join(repository, "one")
	if err := os.Mkdir(filepath.Join(innerRoot, MetadataDirectory), 0o755); err != nil {
		t.Fatal(err)
	}
	innerDatabase := DatabasePath(innerRoot)
	if err := os.WriteFile(innerDatabase, []byte("inner"), 0o644); err != nil {
		t.Fatal(err)
	}

	database, err = FindDatabase(deep)
	if err != nil {
		t.Fatal(err)
	}
	if database.Root != innerRoot || database.Path != innerDatabase {
		t.Fatalf("FindDatabase after nesting = %#v, want root %q and path %q", database, innerRoot, innerDatabase)
	}
}

func TestFindDatabaseReportsTypedNotFound(t *testing.T) {
	t.Parallel()

	start := filepath.Join(t.TempDir(), "no database here", "child")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := FindDatabase(start)
	var public *domain.Error
	if !errors.As(err, &public) {
		t.Fatalf("FindDatabase error = %v, want domain error", err)
	}
	if public.Code != "database_not_found" || public.Kind != domain.NotFound {
		t.Fatalf("FindDatabase error = (%v, %q), want (NotFound, database_not_found)", public.Kind, public.Code)
	}
}
