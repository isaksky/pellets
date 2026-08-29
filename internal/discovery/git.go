package discovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"pellets/internal/domain"
)

const localExcludeEntry = ".pellets/"

// GitSafety implements the local-only Git safeguards used by database
// initialization. It never changes the index, a committed ignore file, or Git
// history.
type GitSafety struct{}

// FindGitRoot resolves the nearest non-bare Git work-tree root using Git's own
// discovery rules. This supports linked worktrees and .git indirection.
func FindGitRoot(ctx context.Context, directory string) (string, error) {
	workTree, ok, err := findWorkTree(ctx, directory)
	if err != nil {
		return "", err
	}
	if !ok {
		absolute, absoluteErr := filepath.Abs(directory)
		if absoluteErr != nil {
			absolute = directory
		}
		return "", domain.NewError(
			domain.NotFound,
			"git_repository_not_found",
			"the current directory is not inside a Git work tree",
			map[string]any{"start_path": filepath.Clean(absolute)},
		)
	}
	canonical, err := resolveExistingPrefix(workTree)
	if err != nil {
		return "", gitInspectionFailure(workTree, err)
	}
	return canonical, nil
}

// RelativeProjectPath returns the canonical, slash-normalized project root
// relative to the database root. Projects outside that root are rejected.
func RelativeProjectPath(databaseRoot, projectRoot string) (string, error) {
	canonicalDatabaseRoot, err := resolveExistingPrefix(databaseRoot)
	if err != nil {
		return "", projectPathFailure(databaseRoot, projectRoot, err)
	}
	canonicalProjectRoot, err := resolveExistingPrefix(projectRoot)
	if err != nil {
		return "", projectPathFailure(databaseRoot, projectRoot, err)
	}
	relative, err := filepath.Rel(canonicalDatabaseRoot, canonicalProjectRoot)
	if err != nil {
		return "", projectPathFailure(databaseRoot, projectRoot, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", domain.NewError(
			domain.Conflict,
			"project_outside_database_root",
			"the Git work tree is outside the selected Pellets database root",
			map[string]any{
				"database_root": canonicalDatabaseRoot,
				"project_root":  canonicalProjectRoot,
			},
		)
	}
	return filepath.ToSlash(relative), nil
}

// RejectTracked rejects the database or any SQLite companion when the
// containing work tree's index already tracks the same filesystem path. A
// database root outside a work tree needs no Git safeguard.
func (GitSafety) RejectTracked(ctx context.Context, databaseRoot, databasePath string) error {
	workTree, ok, err := findWorkTree(ctx, databaseRoot)
	if err != nil || !ok {
		return err
	}
	relativePaths := make([]string, 0, len(DatabasePaths(databasePath)))
	for _, path := range DatabasePaths(databasePath) {
		relative, relativeErr := workTreeRelativeDatabasePath(workTree, databaseRoot, path)
		if relativeErr != nil {
			return gitInspectionFailure(path, relativeErr)
		}
		relativePaths = append(relativePaths, relative)
	}
	caseInsensitive, err := isCaseInsensitiveFilesystem(databaseRoot)
	if err != nil {
		return gitInspectionFailure(databasePath, err)
	}

	output, err := runGit(ctx, workTree, "ls-files", "-z", "--full-name")
	if err != nil {
		return gitInspectionFailure(databasePath, err)
	}
	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		trackedPath := string(entry)
		for _, relative := range relativePaths {
			if trackedPath == relative || caseInsensitive && strings.EqualFold(trackedPath, relative) {
				return domain.NewError(
					domain.Conflict,
					"database_already_tracked",
					"the Pellets database path is already tracked by Git",
					map[string]any{"database_path": databasePath},
				)
			}
		}
	}
	return nil
}

// workTreeRelativeDatabasePath resolves the roots but deliberately does not
// follow a database or companion path. Git's index names the lexical path in
// the work tree, and a pre-existing final-component symlink must not redirect
// the tracked-path inspection elsewhere.
func workTreeRelativeDatabasePath(workTree, databaseRoot, path string) (string, error) {
	canonicalWorkTree, err := resolveExistingPrefix(workTree)
	if err != nil {
		return "", err
	}
	canonicalDatabaseRoot, err := resolveExistingPrefix(databaseRoot)
	if err != nil {
		return "", err
	}
	absoluteDatabaseRoot, err := filepath.Abs(databaseRoot)
	if err != nil {
		return "", err
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	withinRoot, err := filepath.Rel(absoluteDatabaseRoot, absolutePath)
	if err != nil {
		return "", err
	}
	if pathEscapesRoot(withinRoot) {
		return "", fmt.Errorf("path %q is outside database root %q", path, databaseRoot)
	}
	relative, err := filepath.Rel(canonicalWorkTree, filepath.Join(canonicalDatabaseRoot, withinRoot))
	if err != nil {
		return "", err
	}
	if pathEscapesRoot(relative) {
		return "", fmt.Errorf("path %q is outside Git work tree %q", path, workTree)
	}
	return filepath.ToSlash(relative), nil
}

func pathEscapesRoot(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

// EnsureExcluded adds the fixed .pellets/ pattern to the containing work
// tree's repository-local exclude file when it is not already present.
func (GitSafety) EnsureExcluded(ctx context.Context, databaseRoot, databasePath string) error {
	workTree, ok, err := findWorkTree(ctx, databaseRoot)
	if err != nil || !ok {
		return err
	}
	if _, err := workTreeRelativePath(workTree, databasePath); err != nil {
		return gitInspectionFailure(databasePath, err)
	}

	output, err := runGit(ctx, workTree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return gitInspectionFailure(databasePath, err)
	}
	excludePath := trimGitLine(output)
	if excludePath == "" {
		return gitInspectionFailure(databasePath, errors.New("Git returned an empty local exclude path"))
	}
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(workTree, filepath.FromSlash(excludePath))
	}
	excludePath = filepath.Clean(excludePath)

	contents, err := os.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return gitExcludeFailure(excludePath, err)
	}
	for _, line := range bytes.Split(contents, []byte{'\n'}) {
		if string(bytes.TrimSuffix(line, []byte{'\r'})) == localExcludeEntry {
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o777); err != nil {
		return gitExcludeFailure(excludePath, err)
	}
	file, err := os.OpenFile(excludePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		return gitExcludeFailure(excludePath, err)
	}
	addition := []byte(localExcludeEntry + "\n")
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		addition = append([]byte{'\n'}, addition...)
	}
	if _, err := io.Copy(file, bytes.NewReader(addition)); err != nil {
		_ = file.Close()
		return gitExcludeFailure(excludePath, err)
	}
	if err := file.Close(); err != nil {
		return gitExcludeFailure(excludePath, err)
	}
	return nil
}

func findWorkTree(ctx context.Context, directory string) (string, bool, error) {
	output, err := runGit(ctx, directory, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 128 && bytes.Contains(output, []byte("not a git repository")) {
			return "", false, nil
		}
		return "", false, gitInspectionFailure(directory, err)
	}
	if strings.TrimSpace(string(output)) != "true" {
		return "", false, nil
	}

	output, err = runGit(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false, gitInspectionFailure(directory, err)
	}
	workTree := trimGitLine(output)
	if workTree == "" {
		return "", false, gitInspectionFailure(directory, errors.New("Git returned an empty work-tree root"))
	}
	absolute, err := filepath.Abs(workTree)
	if err != nil {
		return "", false, gitInspectionFailure(directory, err)
	}
	return filepath.Clean(absolute), true, nil
}

func workTreeRelativePath(workTree, path string) (string, error) {
	canonicalWorkTree, err := resolveExistingPrefix(workTree)
	if err != nil {
		return "", err
	}
	canonicalPath, err := resolveExistingPrefix(path)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(canonicalWorkTree, canonicalPath)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q is outside Git work tree %q", path, workTree)
	}
	return filepath.ToSlash(relative), nil
}

// isCaseInsensitiveFilesystem determines path identity without creating a
// probe file. That keeps a rejected initialization completely read-only.
func isCaseInsensitiveFilesystem(path string) (bool, error) {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			variant, changed := swapASCIICase(filepath.Base(current))
			if changed {
				variantInfo, variantErr := os.Lstat(filepath.Join(filepath.Dir(current), variant))
				switch {
				case variantErr == nil:
					return os.SameFile(info, variantInfo), nil
				case errors.Is(variantErr, os.ErrNotExist):
					return false, nil
				default:
					return false, variantErr
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	// Windows path comparison is case-insensitive even when no alphabetic
	// existing component was available for the read-only probe.
	return runtime.GOOS == "windows", nil
}

func swapASCIICase(value string) (string, bool) {
	bytes := []byte(value)
	for index, character := range bytes {
		switch {
		case character >= 'a' && character <= 'z':
			bytes[index] = character - ('a' - 'A')
			return string(bytes), true
		case character >= 'A' && character <= 'Z':
			bytes[index] = character + ('a' - 'A')
			return string(bytes), true
		}
	}
	return value, false
}

// resolveExistingPrefix canonicalizes symlinks such as macOS's /var ->
// /private/var even when the final database path does not exist yet.
func resolveExistingPrefix(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func runGit(ctx context.Context, directory string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	command.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func trimGitLine(output []byte) string {
	line := strings.TrimSuffix(string(output), "\n")
	return strings.TrimSuffix(line, "\r")
}

func gitInspectionFailure(path string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"git_inspection_failed",
		"could not inspect Git safety for the Pellets database",
		map[string]any{"path": path},
		err,
	)
}

func gitExcludeFailure(path string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"git_exclude_failed",
		"could not update Git's local exclude file",
		map[string]any{"exclude_path": path},
		err,
	)
}

func projectPathFailure(databaseRoot, projectRoot string, err error) error {
	return domain.WrapError(
		domain.Storage,
		"project_path_resolution_failed",
		"could not normalize the Git project path relative to the Pellets database",
		map[string]any{"database_root": databaseRoot, "project_root": projectRoot},
		err,
	)
}
