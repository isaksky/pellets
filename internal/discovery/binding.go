package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pellets/internal/domain"
)

const databaseBindingName = "pellets-database.json"

type databaseBinding struct {
	Version int    `json:"version"`
	Path    string `json:"path"`
}

// FindDatabase honors a repository's shared binding before ancestor discovery.
// Outside Git, database-level commands retain the common-parent layout.
func FindDatabase(start string) (Database, error) {
	identity, err := FindGitIdentity(context.Background(), start)
	if err != nil {
		if domain.PublicError(err).Code == "git_repository_not_found" {
			return findAncestorDatabase(start)
		}
		return Database{}, err
	}
	if database, found, err := readDatabaseBinding(identity.GitCommonDir); found || err != nil {
		return database, err
	}
	database, err := findAncestorDatabase(start)
	if err != nil && domain.PublicError(err).Code != "database_not_found" {
		return database, err
	}
	// Adopt a pre-binding database reachable from another worktree, including
	// the original checkout. Never silently choose among split legacy queues.
	output, gitErr := runGit(context.Background(), start, "worktree", "list", "--porcelain", "-z")
	if gitErr != nil {
		return Database{}, gitInspectionFailure(start, gitErr)
	}
	var candidate *Database
	if err == nil {
		candidate = &database
	}
	for _, field := range strings.Split(string(output), "\x00") {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		root := strings.TrimPrefix(field, "worktree ")
		if _, statErr := os.Stat(root); errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		found, findErr := findAncestorDatabase(root)
		if findErr != nil {
			if domain.PublicError(findErr).Code == "database_not_found" {
				continue
			}
			return Database{}, findErr
		}
		if candidate != nil && !sameDatabasePath(candidate.Path, found.Path) {
			return Database{}, domain.NewError(domain.Conflict, "database_binding_conflict", "linked worktrees have multiple Pellets databases; select and consolidate the intended database before retrying", map[string]any{"database_paths": []string{candidate.Path, found.Path}})
		}
		candidate = &found
	}
	if candidate != nil {
		return *candidate, nil
	}
	return Database{}, err
}

// WithDatabaseBinding serializes first-use discovery, bootstrap, and binding
// publication across worktrees. Existing bindings require no filesystem writes.
func WithDatabaseBinding(ctx context.Context, start string, bootstrap func() (Database, error)) (Database, error) {
	identity, err := FindGitIdentity(ctx, start)
	if err != nil {
		return Database{}, err
	}
	if _, found, err := readDatabaseBinding(identity.GitCommonDir); err != nil {
		return Database{}, err
	} else if found {
		return bootstrap()
	}
	lockPath := filepath.Join(identity.GitCommonDir, databaseBindingName+".lock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = os.Mkdir(lockPath, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return Database{}, bindingFailure(lockPath, err)
		}
		if time.Now().After(deadline) {
			return Database{}, domain.NewError(domain.Conflict, "database_binding_busy", "database binding is being initialized; retry, or remove the lock directory only after confirming no Pellets setup is running", map[string]any{"lock_path": lockPath})
		}
		select {
		case <-ctx.Done():
			return Database{}, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer os.Remove(lockPath)
	// Rediscovery inside bootstrap observes a binding published by the winner.
	database, err := bootstrap()
	if err != nil {
		return Database{}, err
	}
	if _, found, err := readDatabaseBinding(identity.GitCommonDir); found || err != nil {
		return database, err
	}
	path := database.Path
	if relative, relErr := filepath.Rel(identity.GitCommonDir, path); relErr == nil {
		path = relative
	}
	data, err := json.Marshal(databaseBinding{Version: 1, Path: filepath.ToSlash(path)})
	if err != nil {
		return Database{}, bindingFailure(identity.GitCommonDir, err)
	}
	// Rename a fully written file so readers never observe a partial binding.
	temporary := filepath.Join(lockPath, "binding.json")
	defer os.Remove(temporary)
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return Database{}, bindingFailure(temporary, err)
	}
	bindingPath := filepath.Join(identity.GitCommonDir, databaseBindingName)
	if err := os.Rename(temporary, bindingPath); err != nil {
		return Database{}, bindingFailure(bindingPath, err)
	}
	return database, nil
}

func readDatabaseBinding(commonDir string) (Database, bool, error) {
	path := filepath.Join(commonDir, databaseBindingName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Database{}, false, nil
	}
	if err != nil {
		return Database{}, false, bindingFailure(path, err)
	}
	if !info.Mode().IsRegular() {
		return Database{}, true, bindingFailure(path, errors.New("binding must be a regular file"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Database{}, true, bindingFailure(path, err)
	}
	var binding databaseBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return Database{}, true, bindingFailure(path, err)
	}
	if binding.Version != 1 || binding.Path == "" {
		return Database{}, true, bindingFailure(path, errors.New("unsupported binding version or empty path"))
	}
	target := filepath.FromSlash(binding.Path)
	if !filepath.IsAbs(target) {
		target = filepath.Join(commonDir, target)
	}
	target = filepath.Clean(target)
	if filepath.Base(target) != DatabaseFilename || filepath.Base(filepath.Dir(target)) != MetadataDirectory {
		return Database{}, true, bindingFailure(path, errors.New("bound path must end in .pellets/pellets.db"))
	}
	info, err = os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		return Database{}, true, domain.NewError(domain.Storage, "database_binding_unavailable", "the bound Pellets database is unavailable; restore it or repair the binding before retrying", map[string]any{"binding_path": path, "database_path": target})
	}
	return Database{Root: filepath.Dir(filepath.Dir(target)), Path: target}, true, nil
}

func bindingFailure(path string, cause error) error {
	return domain.WrapError(domain.Storage, "database_binding_failed", "could not read or write the shared Pellets database binding", map[string]any{"binding_path": path}, cause)
}

func sameDatabasePath(left, right string) bool {
	if left == right {
		return true
	}
	a, errA := os.Stat(left)
	b, errB := os.Stat(right)
	return errA == nil && errB == nil && os.SameFile(a, b)
}
