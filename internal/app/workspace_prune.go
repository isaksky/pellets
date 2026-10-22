package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pellets/internal/discovery"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
)

type WorkspacePruneEntry struct {
	ID     int64  `json:"id"`
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}
type WorkspacePruneResult struct {
	DryRun  bool                  `json:"dry_run"`
	Pruned  []WorkspacePruneEntry `json:"pruned"`
	Skipped []WorkspacePruneEntry `json:"skipped"`
}

// The schedule guard lives beside the database, so removing a checkout cannot
// remove its exclusion. Both scheduling and pruning hold it through completion.
func workspaceScheduleGuard(database Database, id int64) (*executionlock.Lock, error) {
	if !filepath.IsAbs(database.Path) {
		return nil, fmt.Errorf("workspace guard requires an absolute database path")
	}
	dir := filepath.Join(filepath.Dir(database.Path), "workspace-guards", fmt.Sprint(id))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return executionlock.Acquire(dir)
}

func PruneWorkspaces(ctx context.Context, database Database, project storage.Project, writer storage.WorkspacePruner, dryRun bool) (WorkspacePruneResult, error) {
	result := WorkspacePruneResult{DryRun: dryRun, Pruned: []WorkspacePruneEntry{}, Skipped: []WorkspacePruneEntry{}}
	for _, w := range project.Workspaces {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entry := WorkspacePruneEntry{ID: w.ID, Path: w.RootPath.Value}
		reason, err := pruneWorkspace(ctx, database, project, w, writer, dryRun)
		if err != nil {
			return result, err
		}
		if reason != "" {
			entry.Reason = reason
			result.Skipped = append(result.Skipped, entry)
		} else {
			result.Pruned = append(result.Pruned, entry)
		}
	}
	return result, nil
}

func pruneWorkspace(ctx context.Context, database Database, project storage.Project, w storage.Workspace, writer storage.WorkspacePruner, dryRun bool) (string, error) {
	if w.GitDir == project.GitCommonDir {
		return "main_workspace", nil
	}
	common, err := discovery.ResolveLocalPath(database.Root, project.GitCommonDir)
	if err != nil {
		return "repository_unavailable", nil
	}
	if info, err := os.Stat(common); err != nil || !info.IsDir() {
		return "repository_unavailable", nil
	}
	root, err := discovery.ResolveLocalPath(database.Root, w.RootPath)
	if err != nil {
		return "path_unavailable", nil
	}
	// Lstat keeps dangling symlinks and permission failures out of the missing set.
	if _, err = os.Lstat(root); err == nil {
		return "checkout_present", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "path_unavailable", nil
	}
	// Agent cleanup often removes the checkout's enclosing temporary folder.
	// A missing parent is sufficient only when Git's worktree metadata is gone
	// too; existing metadata may describe disconnected storage.
	parent, parentErr := os.Stat(filepath.Dir(root))
	parentMissing := errors.Is(parentErr, os.ErrNotExist)
	if (parentErr != nil && !parentMissing) || (parentErr == nil && !parent.IsDir()) {
		return "parent_unavailable", nil
	}
	var guard *executionlock.Lock
	if dryRun {
		dir := filepath.Join(filepath.Dir(database.Path), "workspace-guards", fmt.Sprint(w.ID))
		if _, e := os.Lstat(dir); errors.Is(e, os.ErrNotExist) {
			err = nil
		} else {
			guard, err = executionlock.InspectRecovery(dir)
		}
	} else {
		guard, err = workspaceScheduleGuard(database, w.ID)
	}
	if err != nil {
		return "schedule_busy_or_unavailable", nil
	}
	if guard != nil {
		defer guard.Close()
	}
	gitDir, err := discovery.ResolveLocalPath(database.Root, w.GitDir)
	if err != nil {
		return "path_unavailable", nil
	}
	if _, err := os.Lstat(gitDir); err == nil {
		if parentMissing {
			return "parent_unavailable", nil
		}
		if _, lockErr := os.Lstat(filepath.Join(gitDir, "locked")); lockErr == nil {
			return "git_worktree_locked", nil
		} else if !errors.Is(lockErr, os.ErrNotExist) {
			return "path_unavailable", nil
		}
		// Git updates this back-pointer when a checkout moves. Do not retire
		// its old registration before ordinary bootstrap can repair the path.
		pointer, readErr := os.ReadFile(filepath.Join(gitDir, "gitdir"))
		if readErr == nil {
			pointerRoot, normalizeErr := discovery.NormalizeLocalPath(database.Root, filepath.Dir(strings.TrimSuffix(strings.TrimSuffix(string(pointer), "\n"), "\r")))
			if normalizeErr != nil {
				return "path_unavailable", nil
			}
			if pointerRoot != w.RootPath {
				return "workspace_moved", nil
			}
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return "path_unavailable", nil
		}
		// Retain any receipt, even a completed run whose cleanup is unconfirmed.
		var lock *executionlock.Lock
		if dryRun {
			lock, err = executionlock.InspectRecovery(gitDir)
		} else {
			lock, err = executionlock.AcquireRecovery(gitDir)
		}
		if err != nil {
			return "execution_busy_or_unavailable", nil
		}
		if lock != nil {
			defer lock.Close()
		}
		if lock != nil && lock.Owner() != nil {
			return "recovery_pending", nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "path_unavailable", nil
	}
	// Recheck after acquiring exclusion; storage also checks identity and ownership.
	if _, err = os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return "checkout_changed", nil
	}
	return writer.PruneWorkspace(ctx, w, dryRun)
}

// ReasonLabel is presentation text; JSON retains the stable reason code.
func (entry WorkspacePruneEntry) ReasonLabel() string {
	switch entry.Reason {
	case "git_worktree_locked":
		return "Git worktree is locked"
	case "main_workspace":
		return "Main workspace"
	case "checkout_present":
		return "Checkout still exists"
	case "repository_unavailable":
		return "Repository cannot be checked"
	case "parent_unavailable":
		return "Parent folder cannot be checked"
	case "schedule_busy_or_unavailable":
		return "Schedule is active or its status cannot be checked"
	case "execution_busy_or_unavailable":
		return "Execution is active or its status cannot be checked"
	case "recovery_pending", "work_or_recovery_pending":
		return "Work or recovery needs attention"
	case "already_pruned":
		return "Already pruned"
	case "workspace_changed", "checkout_changed", "workspace_moved":
		return "Workspace moved or changed; kept for inspection"
	default:
		return "Path cannot be checked"
	}
}
