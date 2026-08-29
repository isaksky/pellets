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

func TestFindGitIdentityDistinguishesLinkedWorkspaceFromSharedRepository(t *testing.T) {
	t.Parallel()

	common := filepath.Join(t.TempDir(), "identity root 界")
	mainRoot := filepath.Join(common, "main work tree")
	linkedRoot := filepath.Join(common, "linked 工作 tree")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runDiscoveryGit(t, mainRoot, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(mainRoot, "fixture"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDiscoveryGit(t, mainRoot, "add", "fixture")
	runDiscoveryGit(t, mainRoot, "-c", "user.name=Pellets Test", "-c", "user.email=pellets@example.invalid", "commit", "--quiet", "-m", "fixture")
	runDiscoveryGit(t, mainRoot, "worktree", "add", "--quiet", "-b", "identity-linked", linkedRoot)

	mainIdentity, err := FindGitIdentity(context.Background(), mainRoot)
	if err != nil {
		t.Fatal(err)
	}
	linkedIdentity, err := FindGitIdentity(context.Background(), linkedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if mainIdentity.GitCommonDir != linkedIdentity.GitCommonDir {
		t.Fatalf("common directories differ: %#v %#v", mainIdentity, linkedIdentity)
	}
	if mainIdentity.WorkTreeRoot == linkedIdentity.WorkTreeRoot || mainIdentity.GitDir == linkedIdentity.GitDir {
		t.Fatalf("workspace identities are not distinct: %#v %#v", mainIdentity, linkedIdentity)
	}
	for _, identity := range []domain.GitIdentity{mainIdentity, linkedIdentity} {
		for _, value := range []string{identity.WorkTreeRoot, identity.GitCommonDir, identity.GitDir} {
			if !filepath.IsAbs(value) {
				t.Fatalf("Git identity path is not absolute: %q", value)
			}
		}
	}
}

func TestNormalizeLocalPathUsesRelativeWherePossibleAndAbsoluteOtherwise(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "database root")
	inside := filepath.Join(root, "linked tree")
	outside := filepath.Join(filepath.Dir(root), "outside tree")
	for _, value := range []string{inside, outside} {
		if err := os.MkdirAll(value, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	insidePath, err := NormalizeLocalPath(root, inside)
	if err != nil || !insidePath.Relative || insidePath.Value != "linked tree" {
		t.Fatalf("inside normalized path = (%#v, %v)", insidePath, err)
	}
	outsidePath, err := NormalizeLocalPath(root, outside)
	if err != nil || outsidePath.Relative || !filepath.IsAbs(filepath.FromSlash(outsidePath.Value)) {
		t.Fatalf("outside normalized path = (%#v, %v)", outsidePath, err)
	}
	resolved, err := ResolveLocalPath(root, insidePath)
	if err != nil {
		t.Fatal(err)
	}
	canonicalInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonicalInside {
		t.Fatalf("resolved inside path = %q, want %q", resolved, canonicalInside)
	}
}

func runDiscoveryGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
