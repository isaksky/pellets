package main

import (
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/discovery"
)

func TestDatabaseBindingCompiled(t *testing.T) {
	executable := buildFoundationExecutable(t)
	fixture := func(t *testing.T) (string, string) {
		t.Helper()
		main := filepath.Join(foundationShortTempDir(t), "main")
		linked := filepath.Join(foundationShortTempDir(t), "linked")
		createFoundationRepository(t, main)
		runFoundationGit(t, main, "worktree", "add", "--quiet", "-b", "worker", linked)
		return main, linked
	}
	t.Run("outside worktree shares queue and distinct ownership", func(t *testing.T) {
		main, linked := fixture(t)
		first := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, main, "add", "first"), "add")
		second := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, main, "add", "second"), "add")
		selected := decodeFoundationSuccess[foundationNext](t, runFoundationCLI(t, executable, linked, "start-next"), "start-next")
		other := decodeFoundationSuccess[foundationNext](t, runFoundationCLI(t, executable, main, "start-next"), "start-next")
		if selected.Pellet == nil || other.Pellet == nil || selected.Pellet.ID != first.ID || other.Pellet.ID != second.ID || selected.Pellet.Workspace.ID == other.Pellet.Workspace.ID {
			t.Fatalf("selections: %#v %#v", selected, other)
		}
		if _, err := os.Stat(discovery.DatabasePath(linked)); !os.IsNotExist(err) {
			t.Fatalf("unexpected linked database: %v", err)
		}
		projects := decodeFoundationSuccess[[]foundationProject](t, runFoundationCLI(t, executable, linked, "project", "list"), "project list")
		if len(projects) != 1 {
			t.Fatalf("projects: %#v", projects)
		}
		if err := os.Rename(discovery.DatabasePath(main), discovery.DatabasePath(main)+".saved"); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"start-next"}, {"project", "list"}} {
			decodeFoundationError(t, runFoundationCLI(t, executable, linked, args...), 5, "database_binding_unavailable", "the bound Pellets database is unavailable; restore it or repair the binding before retrying")
		}
		for _, root := range []string{main, linked} {
			if _, err := os.Stat(discovery.DatabasePath(root)); !os.IsNotExist(err) {
				t.Fatalf("missing binding target recreated: %v", err)
			}
		}
	})
	t.Run("adopts existing database without binding", func(t *testing.T) {
		main, linked := fixture(t)
		first := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, main, "add", "existing"), "add")
		if err := os.Remove(filepath.Join(main, ".git", "pellets-database.json")); err != nil {
			t.Fatal(err)
		}
		selected := decodeFoundationSuccess[foundationNext](t, runFoundationCLI(t, executable, linked, "start-next"), "start-next")
		if selected.Pellet == nil || selected.Pellet.ID != first.ID {
			t.Fatalf("selection: %#v", selected)
		}
	})
	t.Run("split legacy databases fail without choosing either queue", func(t *testing.T) {
		main, linked := fixture(t)
		for _, root := range []string{main, linked} {
			decodeFoundationSuccess[map[string]any](t, runFoundationCLI(t, executable, root, "init-db"), "init-db")
		}
		for _, root := range []string{main, linked} {
			decodeFoundationError(t, runFoundationCLI(t, executable, root, "next"), 4, "database_binding_conflict", "linked worktrees have multiple Pellets databases; select and consolidate the intended database before retrying")
		}
		if _, err := os.Stat(filepath.Join(main, ".git", "pellets-database.json")); !os.IsNotExist(err) {
			t.Fatalf("conflict published binding: %v", err)
		}
	})
	t.Run("malformed binding does not fall back to a valid ancestor", func(t *testing.T) {
		main, linked := fixture(t)
		decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, main, "add", "existing"), "add")
		binding := filepath.Join(main, ".git", "pellets-database.json")
		if err := os.WriteFile(binding, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, root := range []string{main, linked} {
			decodeFoundationError(t, runFoundationCLI(t, executable, root, "next"), 5, "database_binding_failed", "could not read or write the shared Pellets database binding")
		}
		if _, err := os.Stat(discovery.DatabasePath(linked)); !os.IsNotExist(err) {
			t.Fatalf("malformed binding created database: %v", err)
		}
	})

	t.Run("simultaneous first use converges across worktrees", func(t *testing.T) {
		main, linked := fixture(t)
		results := runCoreQueueCLIConcurrently(t, executable, []coreQueueInvocation{{directory: main, args: []string{"add", "one"}}, {directory: linked, args: []string{"add", "two"}}})
		a := decodeFoundationSuccess[foundationPellet](t, results[0], "add")
		b := decodeFoundationSuccess[foundationPellet](t, results[1], "add")
		if a.Project != b.Project || a.Number == b.Number {
			t.Fatalf("split queues: %#v %#v", a, b)
		}
		count := 0
		for _, root := range []string{main, linked} {
			if _, err := os.Stat(discovery.DatabasePath(root)); err == nil {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("database count: %d", count)
		}
	})
	t.Run("common parent remains shared and binding wins over nearer database", func(t *testing.T) {
		main, linked := fixture(t)
		parent := filepath.Dir(main)
		decodeFoundationSuccess[map[string]any](t, runFoundationCLI(t, executable, parent, "init-db"), "init-db")
		first := decodeFoundationSuccess[foundationPellet](t, runFoundationCLI(t, executable, main, "add", "shared"), "add")
		decodeFoundationSuccess[map[string]any](t, runFoundationCLI(t, executable, linked, "init-db"), "init-db")
		selected := decodeFoundationSuccess[foundationNext](t, runFoundationCLI(t, executable, linked, "next"), "next")
		if selected.Pellet == nil || selected.Pellet.ID != first.ID {
			t.Fatalf("selection: %#v", selected)
		}
	})
}
