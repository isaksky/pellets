package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"pellets/internal/storage"
)

func reviewSideEffect() error {
	return scheduleError("review_side_effect_detected", "repository HEAD, index, worktree, or unattributed refs changed during the read-only review; preserve and inspect the changes")
}

func reviewRefsDigest(refs string) string {
	digest := sha256.Sum256([]byte(refs))
	return hex.EncodeToString(digest[:])
}

const reviewRefSampleAttempts = 3

func reviewRepositoryRefs(ctx context.Context, root string) (string, error) {
	return executionGitRaw(ctx, root, "for-each-ref", "--format=%(refname)%00%(objectname)%00")
}

// Read a coherent ref/log baseline. An intermediate historical occurrence is
// not a baseline: it could be an unattributed rewind rather than a stale read.
func captureReviewRefContext(ctx context.Context, root, refs string) (*storage.ReviewRefContext, string, error) {
	for attempt := 0; attempt < reviewRefSampleAttempts; attempt++ {
		guard, coherent, err := captureReviewRefContextOnce(ctx, root, refs)
		if err != nil {
			return nil, "", err
		}
		latest, err := reviewRepositoryRefs(ctx, root)
		if err != nil {
			return nil, "", err
		}
		if coherent && latest == refs {
			return guard, refs, nil
		}
		refs = latest
	}
	return nil, "", scheduleError("review_repository_state_unavailable", "repository refs and worktree HEAD reflogs do not provide a stable, consistent review snapshot")
}

// Only branches already attached to a distinct worktree can receive an
// exception. New refs, tags, replace refs and unattached branches remain in the
// original all-refs digest. A missing reflog grants no exception.
func captureReviewRefContextOnce(ctx context.Context, root, refs string) (*storage.ReviewRefContext, bool, error) {
	headRef, err := executionGit(ctx, root, "rev-parse", "--symbolic-full-name", "HEAD")
	if err != nil {
		return nil, false, err
	}
	worktrees, err := reviewOtherWorktrees(ctx, root, headRef)
	if err != nil {
		return nil, false, err
	}
	result := &storage.ReviewRefContext{HeadReference: headRef}
	for _, worktree := range worktrees {
		worktree.Head = reviewRefHead(refs, worktree.Branch)
		if !storage.IsFullCommitID(worktree.Head) {
			continue
		}
		log, err := reviewHeadLog(ctx, worktree.Root)
		if err != nil || len(log) == 0 || log[len(log)-1] != '\n' {
			continue
		}
		last := bytes.TrimSuffix(log, []byte{'\n'})
		last = last[bytes.LastIndexByte(last, '\n')+1:]
		fields := strings.Fields(string(last))
		if len(fields) < 2 || fields[1] != worktree.Head {
			return nil, false, nil
		}
		worktree.HeadLogBytes = int64(len(log))
		worktree.HeadLogSHA256 = reviewRefsDigest(string(log))
		result.Worktrees = append(result.Worktrees, worktree)
	}
	return result, true, nil
}

func reviewRefLine(branch, head string) string { return branch + "\x00" + head + "\x00\n" }

func reviewRefHead(refs, branch string) string {
	for _, line := range strings.Split(refs, "\n") {
		name, value, ok := strings.Cut(line, "\x00")
		if ok && name == branch {
			return strings.TrimSuffix(value, "\x00")
		}
	}
	return ""
}

func reviewOtherWorktrees(ctx context.Context, root, headRef string) ([]storage.ReviewWorktreeRef, error) {
	output, err := executionGitRaw(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var result []storage.ReviewWorktreeRef
	for _, record := range strings.Split(output, "\x00\x00") {
		var worktree storage.ReviewWorktreeRef
		for _, field := range strings.Split(record, "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				worktree.Root = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "branch "):
				worktree.Branch = strings.TrimPrefix(field, "branch ")
			case strings.HasPrefix(field, "HEAD "):
				worktree.Head = strings.TrimPrefix(field, "HEAD ")
			}
		}
		if worktree.Root != "" && filepath.Clean(worktree.Root) != filepath.Clean(root) && strings.HasPrefix(worktree.Branch, "refs/heads/") && worktree.Branch != headRef && storage.IsFullCommitID(worktree.Head) {
			result = append(result, worktree)
		}
	}
	return result, nil
}

func reviewHeadLog(ctx context.Context, root string) ([]byte, error) {
	gitDir, err := executionGit(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(gitDir, "logs", "HEAD"))
}

func verifyReviewRefs(ctx context.Context, root, refs string, snapshot *storage.ReviewSnapshot) error {
	var validationErr error
	for attempt := 0; attempt < reviewRefSampleAttempts; attempt++ {
		validationErr = verifyReviewRefsOnce(ctx, root, refs, snapshot)
		latest, err := reviewRepositoryRefs(ctx, root)
		if err != nil {
			return err
		}
		if validationErr == nil && latest == refs {
			return nil
		}
		refs = latest
	}
	if validationErr != nil {
		return validationErr
	}
	return reviewSideEffect()
}

func verifyReviewRefsOnce(ctx context.Context, root, refs string, snapshot *storage.ReviewSnapshot) error {
	if guard := snapshot.RepositoryRefContext; guard != nil {
		headRef, err := executionGit(ctx, root, "rev-parse", "--symbolic-full-name", "HEAD")
		if err != nil {
			return err
		}
		if headRef != guard.HeadReference {
			return reviewSideEffect()
		}
		worktrees, err := reviewOtherWorktrees(ctx, root, headRef)
		if err != nil {
			return err
		}
		for _, before := range guard.Worktrees {
			for _, after := range worktrees {
				after.Head = reviewRefHead(refs, after.Branch)
				if before.Root != after.Root || before.Branch != after.Branch {
					continue
				}
				if !storage.IsFullCommitID(after.Head) || !reviewBranchAdvance(ctx, root, before, after) {
					return reviewSideEffect()
				}
				refs = strings.TrimPrefix(strings.Replace("\n"+refs, "\n"+reviewRefLine(after.Branch, after.Head), "\n"+reviewRefLine(before.Branch, before.Head), 1), "\n")
			}
		}
	}
	if reviewRefsDigest(refs) != snapshot.RepositoryRefsSHA256 {
		return reviewSideEffect()
	}
	return nil
}

// HEAD reflogs are private to each worktree. Require an unchanged prefix and
// an uninterrupted chain of ordinary commits matching the branch advance.
// update-ref from the review worktree cannot supply this evidence. Missing,
// rewritten, expired, reset or ambiguous history fails closed, including Resume.
func reviewBranchAdvance(ctx context.Context, root string, before, after storage.ReviewWorktreeRef) bool {
	log, err := reviewHeadLog(ctx, after.Root)
	if err != nil || before.HeadLogBytes < 1 || before.HeadLogBytes > int64(len(log)) || reviewRefsDigest(string(log[:before.HeadLogBytes])) != before.HeadLogSHA256 || log[len(log)-1] != '\n' {
		return false
	}
	if before.HeadLogBytes == int64(len(log)) {
		return before.Head == after.Head
	}
	head := before.Head
	for _, line := range strings.Split(strings.TrimSuffix(string(log[before.HeadLogBytes:]), "\n"), "\n") {
		metadata, message, ok := strings.Cut(line, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) < 2 || fields[0] != head || !storage.IsFullCommitID(fields[1]) || !strings.HasPrefix(message, "commit: ") {
			return false
		}
		parents, err := executionGit(ctx, root, "rev-list", "--parents", "-n", "1", fields[1], "--")
		ids := strings.Fields(parents)
		if err != nil || len(ids) != 2 || ids[0] != fields[1] || ids[1] != head {
			return false
		}
		head = fields[1]
	}
	// Only the final reflog tip can explain the actual branch value. An
	// intermediate match would also accept a reviewer rewinding the shared ref.
	return head == after.Head
}
