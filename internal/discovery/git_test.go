package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
)

func TestFindGitRootUsesNearestRepositoryAndUnicodePath(t *testing.T) {
	t.Parallel()

	outer := filepath.Join(t.TempDir(), "outer repository")
	inner := filepath.Join(outer, "nested 界 repository")
	deep := filepath.Join(inner, "deep path")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	runDiscoveryGit(t, outer, "init", "--quiet")
	runDiscoveryGit(t, inner, "init", "--quiet")

	got, err := FindGitRoot(context.Background(), deep)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(inner)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("FindGitRoot() = %q, want %q", got, want)
	}
}

func TestFindGitRootRejectsNonRepository(t *testing.T) {
	t.Parallel()

	_, err := FindGitRoot(context.Background(), t.TempDir())
	if err == nil || domain.PublicError(err).Code != "git_repository_not_found" {
		t.Fatalf("FindGitRoot() error = %v", err)
	}
}

func TestRelativeProjectPathNormalizesAndRejectsOutside(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "database 界")
	project := filepath.Join(root, "repositories", "project with spaces")
	outside := filepath.Join(filepath.Dir(root), "outside")
	for _, directory := range []string{project, outside} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := RelativeProjectPath(root, project)
	if err != nil {
		t.Fatal(err)
	}
	if got != "repositories/project with spaces" {
		t.Fatalf("RelativeProjectPath() = %q", got)
	}
	if _, err := RelativeProjectPath(root, outside); err == nil || domain.PublicError(err).Code != "project_outside_database_root" {
		t.Fatalf("outside RelativeProjectPath() error = %v", err)
	}
}

func runDiscoveryGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
