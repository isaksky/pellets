package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"pellets/internal/codex"
	"slices"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func executionSelection(run storage.ExecutionRun) storage.ResolvedProject {
	return storage.ResolvedProject{Project: storage.Project{ID: run.ProjectID, Code: run.ProjectCode, GitCommonDir: run.GitCommonDir}, Workspace: storage.Workspace{ID: run.WorkspaceID, ProjectID: run.ProjectID, RootPath: run.WorkspaceRoot, GitDir: run.WorkspaceGitDir}}
}

func (s *Scheduler) requireOwnership(ctx context.Context, run storage.ExecutionRun) error {
	queue, err := s.options.OpenQueue(ctx, s.options.Database.Path)
	if err != nil {
		return err
	}
	p, err := queue.ReadPellet(ctx, executionSelection(run), domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber})
	err = errors.Join(err, queue.Close())
	if err != nil {
		return err
	}
	if p.Status != domain.PelletInProgress || p.Workspace == nil || p.Workspace.ID != run.WorkspaceID || p.Title != run.PelletTitle || p.Description != run.PelletDescription || !storage.MatchesSchedule(p, run.ExternalID, run.Group) {
		return scheduleError("implementation_ownership_changed", "the exact pellet's ownership or scope changed")
	}
	return nil
}

func requireHead(ctx context.Context, root, expected string) error {
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != expected {
		return errors.Join(missingRunEvidence("implementation_head_changed"), err)
	}
	return nil
}

func requireCleanWorktree(ctx context.Context, root string) error {
	status, err := executionGit(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return scheduleError("implementation_worktree_dirty", "new ordinary work requires a clean index and worktree, including untracked files")
	}
	return nil
}

// Paths are literal identities, never pathspec expressions or directories.
func validateImplementationFiles(files []string) error {
	if len(files) == 0 {
		return scheduleError("implementation_no_changes", "ready without a committable change requires attention")
	}
	if len(files) > 10000 {
		return storage.InvalidExecutionRun("too many implementation files")
	}
	seen := map[string]bool{}
	for _, file := range files {
		if file == "" || file == "." || path.Clean(file) != file || path.IsAbs(file) || strings.ContainsAny(file, "\x00\\") || strings.HasPrefix(file, "../") || seen[file] {
			return scheduleError("implementation_files_invalid", "implementation file identities must be unique relative paths")
		}
		for _, part := range strings.Split(strings.ToLower(file), "/") {
			if part == ".pellets" || part == ".git" {
				return scheduleError("implementation_files_invalid", "repository metadata cannot be finalized")
			}
		}
		seen[file] = true
	}
	return nil
}

func changedFiles(ctx context.Context, root string) ([]string, error) {
	var files []string
	for _, args := range [][]string{{"diff", "--name-only", "--no-renames", "-z", "HEAD", "--"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
		output, err := executionGitRaw(ctx, root, args...)
		if err != nil {
			return nil, err
		}
		if output != "" {
			files = append(files, strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")...)
		}
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}

func requireExactFiles(ctx context.Context, root string, files []string) error {
	actual, err := changedFiles(ctx, root)
	if err != nil {
		return err
	}
	expected := slices.Clone(files)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return scheduleError("implementation_files_changed", "worktree changes do not match the exact implementation files")
	}
	return nil
}

// A private index computes the expected complete tree without changing the
// workspace index. Git handles filters, file modes, symlinks, and deletions.
func implementationTree(ctx context.Context, root, head string, files []string) (string, error) {
	dir, err := os.MkdirTemp("", "pellets-finalization-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"--no-replace-objects", "--literal-pathspecs", "-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
		output, diagnostic, err := codex.RunOwnedCommand(ctx, cmd)
		if err != nil {
			return "", newExecutionGitFailure(err, diagnostic)
		}
		return strings.TrimSpace(output), nil
	}
	if _, err = git("read-tree", head); err != nil {
		return "", err
	}
	if _, err = git(append([]string{"add", "--"}, files...)...); err != nil {
		return "", err
	}
	return git("write-tree")
}

func (s *Scheduler) prepareFinalization(ctx context.Context, execution *WorkspaceExecution, run storage.ExecutionRun, files []string) error {
	if err := s.requireOwnership(ctx, run); err != nil {
		return err
	}
	root, err := executionRoot(ctx, s.options.Database, run)
	if err != nil {
		return err
	}
	if err := requireHead(ctx, root, run.StartingHead); err != nil {
		return err
	}
	if err := validateImplementationFiles(files); err != nil {
		return err
	}
	if err := requireExactFiles(ctx, root, files); err != nil {
		return err
	}
	// The implementation phase must not stage anything.
	staged, err := executionGit(ctx, root, "diff", "--cached", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return err
	}
	if staged != "" {
		return scheduleError("implementation_index_changed", "implementation staged changes before finalization")
	}
	tree, err := implementationTree(ctx, root, run.StartingHead, files)
	if err != nil {
		return err
	}
	startingTree, err := executionGit(ctx, root, "rev-parse", run.StartingHead+"^{tree}")
	if err != nil {
		return err
	}
	if tree == startingTree {
		return scheduleError("implementation_no_changes", "ready without a committable change requires attention")
	}
	progress := run.RunProgress
	progress.Phase = "verification"
	progress.Summary = "Structured implementation result and exact changed files verified."
	progress.Finalization = &storage.FinalizationEvidence{Files: slices.Clone(files), Tree: tree, Subject: runReference(run) + ": implement pellet"}
	run, err = execution.Save(ctx, progress, run.Revision)
	if err != nil {
		return err
	}
	return s.finalize(ctx, execution, run)
}

func (s *Scheduler) finalize(ctx context.Context, execution *WorkspaceExecution, run storage.ExecutionRun) error {
	closeRecovery := run.ResumeFrom != nil && run.Phase == "close"
	f := run.Finalization
	if f == nil || run.ThreadID == "" || run.TurnID == "" {
		return missingRunEvidence("finalization_evidence_missing")
	}
	if err := validateImplementationFiles(f.Files); err != nil {
		return err
	}
	root, err := executionRoot(ctx, s.options.Database, run)
	if err != nil {
		return err
	}
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if run.ResultCommit != "" && head != run.ResultCommit {
		return missingRunEvidence("result_commit_not_head")
	}
	if head == run.StartingHead {
		if err := s.requireOwnership(ctx, run); err != nil {
			return err
		}
		if err := requireExactFiles(ctx, root, f.Files); err != nil {
			return err
		}
		tree, err := implementationTree(ctx, root, run.StartingHead, f.Files)
		if err != nil {
			return err
		}
		if tree != f.Tree {
			return missingRunEvidence("finalization_tree_changed")
		}
		indexBefore, err := executionGit(ctx, root, "write-tree")
		if err != nil {
			return err
		}
		startingTree, err := executionGit(ctx, root, "rev-parse", run.StartingHead+"^{tree}")
		if err != nil {
			return err
		}
		// git add writes the index atomically. A resumed staging boundary can
		// see either our original index or our exact expected tree, never an
		// independently staged hunk that would be overwritten by add.
		if indexBefore != startingTree && indexBefore != f.Tree {
			return missingRunEvidence("finalization_index_changed")
		}
		progress := run.RunProgress
		progress.Phase, progress.Summary = "commit", "Exact tree verified; staging and one commit pending."
		run, err = execution.Save(ctx, progress, run.Revision)
		if err != nil {
			return err
		}
		if _, err = executionGit(ctx, root, append([]string{"--literal-pathspecs", "add", "--"}, f.Files...)...); err != nil {
			return err
		}
		indexTree, err := executionGit(ctx, root, "write-tree")
		if err != nil {
			return err
		}
		if indexTree != f.Tree {
			return missingRunEvidence("finalization_index_changed")
		}
		if err := requireHead(ctx, root, run.StartingHead); err != nil {
			return err
		}
		if err := s.requireOwnership(ctx, run); err != nil {
			return err
		}
		if _, err = executionGit(ctx, root, "commit", "-m", f.Subject); err != nil {
			return errors.Join(scheduleError("finalization_commit_unconfirmed", "commit failed or its result is uncertain; preserve the index and reconcile this attempt"), err)
		}
		head, err = executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return err
		}
	}
	// This same branch reconciles a crash immediately after Git committed,
	// even if recording the result commit never succeeded. It never recommits.
	if err := validateFinalizationCommit(ctx, root, run, head); err != nil {
		return err
	}
	if err := requireCleanWorktree(ctx, root); err != nil {
		return err
	}
	run, err = execution.VerifyCommit(ctx, run.Revision, head)
	if err != nil {
		return err
	}
	progress := run.RunProgress
	progress.Phase, progress.Summary = "close", "Exact result commit verified; closing the bound pellet pending."
	run, err = execution.Save(ctx, progress, run.Revision)
	if err != nil {
		return err
	}
	closed := false
	if closeRecovery {
		queue, err := s.options.OpenQueue(ctx, s.options.Database.Path)
		if err != nil {
			return err
		}
		p, err := queue.ReadPellet(ctx, executionSelection(run), domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber})
		err = errors.Join(err, queue.Close())
		if err != nil {
			return err
		}
		closed = p.Status == domain.PelletClosed && p.Title == run.PelletTitle && p.Description == run.PelletDescription
	}
	if !closed {
		if err := s.requireOwnership(ctx, run); err != nil {
			return err
		}
		if err := requireHead(ctx, root, head); err != nil {
			return err
		}
		queue, err := s.options.OpenQueue(ctx, s.options.Database.Path)
		if err != nil {
			return err
		}
		_, err = queue.TransitionPellet(ctx, executionSelection(run), domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber}, storage.PelletLifecycleRequest{Operation: storage.PelletClose})
		err = errors.Join(err, queue.Close())
		if err != nil {
			return err
		}
	}
	if err := s.validateResult(ctx, run, executionSelection(run)); err != nil {
		return err
	}
	progress = run.RunProgress
	progress.Phase, progress.State, progress.Outcome, progress.Summary = "finalization", "completed", "succeeded", "Implementation, exact single commit, clean worktree, and closed pellet verified."
	_, err = execution.Save(ctx, progress, run.Revision)
	return err
}

func validateFinalizationCommit(ctx context.Context, root string, run storage.ExecutionRun, commit string) error {
	if commit == run.StartingHead || run.Finalization == nil {
		return missingRunEvidence("result_commit_unchanged")
	}
	parent, err := executionGit(ctx, root, "show", "-s", "--format=%P", commit)
	if err != nil || parent != run.StartingHead {
		return errors.Join(missingRunEvidence("finalization_parent_changed"), err)
	}
	tree, err := executionGit(ctx, root, "rev-parse", commit+"^{tree}")
	if err != nil || tree != run.Finalization.Tree {
		return errors.Join(missingRunEvidence("finalization_tree_changed"), err)
	}
	subject, err := executionGit(ctx, root, "show", "-s", "--format=%s", commit)
	if err != nil || subject != run.Finalization.Subject {
		return errors.Join(missingRunEvidence("finalization_subject_changed"), err)
	}
	return nil
}
