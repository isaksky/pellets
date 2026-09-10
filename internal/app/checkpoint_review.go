package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

type checkpointReviewer struct {
	database  Database
	openQueue func(context.Context, string) (storage.SchedulerQueue, error)
}

// NewCheckpointReviewPolicy wires the production app-server reviewer into the
// scheduler. Finding-to-pellet triage intentionally remains a later policy.
func NewCheckpointReviewPolicy(database Database, openQueue func(context.Context, string) (storage.SchedulerQueue, error)) *CheckpointExecutionPolicy {
	r := &checkpointReviewer{database: database, openQueue: openQueue}
	return &CheckpointExecutionPolicy{Drive: r.drive, Resume: r.resume, ValidateCompletion: r.validateCompletion}
}

func (r *checkpointReviewer) drive(ctx context.Context, execution *WorkspaceExecution) error {
	run, err := execution.Read(ctx)
	if err != nil {
		return err
	}
	if run.CheckpointScope == nil || !run.CheckpointScope.Ready {
		return scheduleError("review_checkpoint_not_ready", "the immutable checkpoint scope is not ready for review")
	}
	root, err := executionRoot(ctx, r.database, run)
	if err != nil {
		return err
	}
	if err := r.requireScope(ctx, run); err != nil {
		return err
	}
	snapshot, err := buildReviewSnapshot(ctx, root, run)
	if err != nil {
		return err
	}
	progress := run.RunProgress
	progress.Phase, progress.Summary, progress.ReviewSnapshot = "review", "Exact review scope and repository instructions captured.", snapshot
	run, err = execution.Save(ctx, progress, run.Revision)
	if err != nil {
		return err
	}

	// Detached review forks the supplied thread, so start from a new empty seed
	// thread instead of the implementer's conversation. Read-only is a strict
	// per-thread reduction; automatic approval review remains configured.
	params := execution.ThreadStartParams()
	params["ephemeral"] = false
	params["sandbox"] = "read-only"
	if _, err = execution.Call(ctx, codex.ThreadStart, params); err != nil {
		return scheduleError("review_seed_unconfirmed", "the fresh empty review seed conversation could not be confirmed")
	}
	run, err = execution.Read(ctx)
	if err != nil {
		return err
	}
	prompt, err := reviewPrompt(run, snapshot)
	if err != nil {
		return err
	}
	params = map[string]any{"threadId": run.ThreadID, "delivery": "detached", "target": map[string]any{"type": "custom", "instructions": prompt}}
	if _, err = execution.Call(ctx, codex.ReviewStart, params); err != nil {
		return scheduleError("review_start_unconfirmed", "the separate detached review conversation could not be confirmed")
	}
	return r.consume(ctx, execution, root)
}

func (r *checkpointReviewer) resume(ctx context.Context, execution *WorkspaceExecution) error {
	run, err := execution.Read(ctx)
	if err != nil {
		return err
	}
	// review/start is never replayed. A durable result emitted before an
	// interruption can be reconciled and closed after all evidence is rechecked.
	if run.ReviewSnapshot == nil || run.ReviewResult == nil {
		return scheduleError("review_resume_evidence_missing", "the interrupted review lacks a durable final reviewer result; inspect the saved review conversation before continuing")
	}
	if err := r.verifyUnchanged(ctx, run); err != nil {
		return err
	}
	_, err = execution.CompleteReviewCheckpoint(ctx, run.Revision)
	return err
}

func (r *checkpointReviewer) consume(ctx context.Context, execution *WorkspaceExecution, root string) error {
	run, err := execution.Read(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case action := <-execution.actions:
			action.result <- interactionResult{run: run, err: storage.InvalidExecutionRun("checkpoint reviews do not accept steering or interaction decisions")}
		case event, ok := <-execution.Events():
			if !ok {
				return codex.ErrClosed
			}
			if len(event.ID) != 0 {
				_ = execution.Respond(ctx, event.ID, nil, &codex.RPCError{Code: -32600, Message: "Pellets reviews are read-only and cannot authorize interactions"})
				return scheduleError("review_interaction_forbidden", "the reviewer requested an interaction outside the read-only review boundary")
			}
			if resolved := resolvedRequestID(event); run.Interaction != nil && resolved == run.ThreadID+"\x00"+run.Interaction.RequestID {
				progress := run.RunProgress
				progress.State, progress.Interaction, progress.Summary = "running", nil, "The pending Codex request was resolved or withdrawn."
				run, err = execution.Save(ctx, progress, run.Revision)
				if err != nil {
					return err
				}
				continue
			}
			if result := parseReviewResult(event, run.ThreadID, run.TurnID, root, run.ReviewSnapshot); result != nil {
				if run.ReviewResult != nil {
					return scheduleError("review_result_invalid", "the review emitted multiple final results")
				}
				progress := run.RunProgress
				progress.ReviewResult, progress.Summary = result, "Reviewer output received; validating exact scope and side effects."
				run, err = execution.Save(ctx, progress, run.Revision)
				if err != nil {
					return err
				}
			}
			if summary := conciseReviewActivity(event, run.ThreadID, run.TurnID); summary != "" && summary != run.Summary {
				progress := run.RunProgress
				progress.Summary = summary
				run, err = execution.Save(ctx, progress, run.Revision)
				if err != nil {
					return err
				}
			}
			status := completedTurnStatus(&event, run.ThreadID, run.TurnID)
			if status == "" {
				continue
			}
			if status != "completed" {
				return scheduleError("codex_review_unsuccessful", "the exact Codex review turn did not complete successfully")
			}
			if run.ReviewResult == nil {
				return scheduleError("review_result_invalid", "the exact review turn lacks a structured final result")
			}
			if err := r.verifyUnchanged(ctx, run); err != nil {
				return err
			}
			_, err = execution.CompleteReviewCheckpoint(ctx, run.Revision)
			return err
		}
	}
}

func (r *checkpointReviewer) requireScope(ctx context.Context, run storage.ExecutionRun) error {
	if r.openQueue == nil {
		return scheduleError("checkpoint_policy_required", "checkpoint storage is unavailable")
	}
	queue, err := r.openQueue(ctx, r.database.Path)
	if err != nil {
		return err
	}
	pellet, readErr := queue.ReadPellet(ctx, executionSelection(run), domain.PelletReference{ProjectCode: run.ProjectCode, Number: run.PelletNumber})
	err = errors.Join(readErr, queue.Close())
	if err != nil {
		return err
	}
	owned := pellet.Status == domain.PelletInProgress && pellet.Workspace != nil && pellet.Workspace.ID == run.WorkspaceID
	reconciling := run.ResumeFrom != nil && pellet.Status == domain.PelletClosed && pellet.Workspace == nil && run.ReviewSnapshot != nil && run.ReviewResult != nil
	if (!owned && !reconciling) || pellet.ImplementationRevision != run.ImplementationRevision || !storage.SameReviewScope(pellet.Checkpoint, run.CheckpointScope) || pellet.Checkpoint == nil || !pellet.Checkpoint.Ready {
		return storage.ExecutionRunConflict(run.ID)
	}
	return nil
}

func (r *checkpointReviewer) verifyUnchanged(ctx context.Context, run storage.ExecutionRun) error {
	if err := r.requireScope(ctx, run); err != nil {
		return err
	}
	root, err := executionRoot(ctx, r.database, run)
	if err != nil {
		return err
	}
	head, statusHash, refsHash, err := reviewRepositoryState(ctx, root)
	if err != nil {
		return err
	}
	if run.ReviewSnapshot == nil || head != run.ReviewSnapshot.RepositoryHead || statusHash != run.ReviewSnapshot.RepositoryStatusSHA256 || refsHash != run.ReviewSnapshot.RepositoryRefsSHA256 {
		return scheduleError("review_side_effect_detected", "repository HEAD, index, or worktree changed during the read-only review; preserve and inspect the changes")
	}
	for _, commit := range run.ReviewSnapshot.Commits {
		if err := verifyRunCommit(ctx, root, commit.StartingHead); err != nil {
			return err
		}
		if err := verifyRunCommit(ctx, root, commit.ResultCommit); err != nil {
			return err
		}
	}
	return nil
}

func buildReviewSnapshot(ctx context.Context, root string, run storage.ExecutionRun) (*storage.ReviewSnapshot, error) {
	head, statusHash, refsHash, err := reviewRepositoryState(ctx, root)
	if err != nil {
		return nil, err
	}
	snapshot := &storage.ReviewSnapshot{Version: 1, RepositoryHead: head, RepositoryStatusSHA256: statusHash, RepositoryRefsSHA256: refsHash, Targets: slices.Clone(run.CheckpointScope.Targets)}
	seenInstructions := map[string]bool{}
	for _, target := range snapshot.Targets {
		if target.Reason != "ready" || target.Evidence == nil {
			return nil, missingRunEvidence("review_commit_evidence_missing")
		}
		e := target.Evidence
		if err := verifyRunCommit(ctx, root, e.StartingHead); err != nil {
			return nil, err
		}
		if err := verifyRunCommit(ctx, root, e.ResultCommit); err != nil {
			return nil, err
		}
		files, err := reviewCommitFiles(ctx, root, e.ResultCommit)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			return nil, missingRunEvidence("review_commit_empty")
		}
		snapshot.Commits = append(snapshot.Commits, storage.ReviewCommit{Reference: target.Reference, WorkspaceID: e.WorkspaceID, StartingHead: e.StartingHead, ResultCommit: e.ResultCommit, Files: files})
		for _, file := range files {
			for _, instructionPath := range applicableInstructionPaths(file) {
				key := e.ResultCommit + "\x00" + instructionPath
				if seenInstructions[key] {
					continue
				}
				text, found, err := gitFileAtCommit(ctx, root, e.ResultCommit, instructionPath)
				if err != nil {
					return nil, err
				}
				if !found {
					continue
				}
				seenInstructions[key] = true
				digest := sha256.Sum256([]byte(text))
				snapshot.Instructions = append(snapshot.Instructions, storage.ReviewInstruction{Commit: e.ResultCommit, Path: instructionPath, SHA256: hex.EncodeToString(digest[:]), Text: text})
			}
		}
	}
	if err := storage.ValidateReviewSnapshot(snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func reviewRepositoryState(ctx context.Context, root string) (string, string, string, error) {
	head, err := executionGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", "", "", err
	}
	status, err := executionGitRaw(ctx, root, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", "", "", err
	}
	index, err := executionGitRaw(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return "", "", "", err
	}
	paths, err := executionGitRaw(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return "", "", "", err
	}
	stateDigest, err := reviewWorktreeDigest(ctx, root, status, index, paths)
	if err != nil {
		return "", "", "", err
	}
	refs, err := executionGitRaw(ctx, root, "for-each-ref", "--format=%(refname)%00%(objectname)%00")
	if err != nil {
		return "", "", "", err
	}
	refsDigest := sha256.Sum256([]byte(refs))
	return head, stateDigest, hex.EncodeToString(refsDigest[:]), nil
}

// Porcelain status does not identify the contents of an already-dirty file.
// Include the exact index plus content hashes for all tracked and unignored
// worktree paths so a reviewer cannot hide a write behind an unchanged status.
func reviewWorktreeDigest(ctx context.Context, root, status, index, encodedPaths string) (string, error) {
	digest := sha256.New()
	writeDigestChunk(digest, []byte(status))
	writeDigestChunk(digest, []byte(index))
	paths := strings.Split(strings.TrimSuffix(encodedPaths, "\x00"), "\x00")
	if encodedPaths == "" {
		paths = nil
	}
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		local := filepath.Join(root, filepath.FromSlash(name))
		relative, err := filepath.Rel(root, local)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", scheduleError("review_repository_state_unavailable", "a Git path escapes the recorded review worktree")
		}
		writeDigestChunk(digest, []byte(name))
		info, err := os.Lstat(local)
		if os.IsNotExist(err) {
			writeDigestChunk(digest, []byte("missing"))
			continue
		}
		if err != nil {
			return "", scheduleError("review_repository_state_unavailable", "a Git path cannot be read for the immutable review snapshot")
		}
		writeDigestChunk(digest, []byte(info.Mode().String()))
		switch {
		case info.Mode().IsRegular():
			file, err := os.Open(local)
			if err != nil {
				return "", scheduleError("review_repository_state_unavailable", "a tracked or unignored file cannot be read for the immutable review snapshot")
			}
			openedInfo, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, openedInfo) {
				_ = file.Close()
				return "", scheduleError("review_repository_state_unavailable", "a tracked or unignored file changed while its review snapshot was captured")
			}
			fileDigest := sha256.New()
			_, copyErr := io.Copy(fileDigest, file)
			closeErr := file.Close()
			if copyErr != nil || closeErr != nil {
				return "", scheduleError("review_repository_state_unavailable", "a tracked or unignored file changed or became unreadable while its review snapshot was captured")
			}
			writeDigestChunk(digest, fileDigest.Sum(nil))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(local)
			if err != nil {
				return "", scheduleError("review_repository_state_unavailable", "a Git symlink cannot be read for the immutable review snapshot")
			}
			writeDigestChunk(digest, []byte(target))
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeDigestChunk(digest io.Writer, data []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(data)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(data)
}

func reviewCommitFiles(ctx context.Context, root, commit string) ([]string, error) {
	output, err := executionGitRaw(ctx, root, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-m", "-z", commit, "--")
	if err != nil {
		return nil, err
	}
	if output == "" {
		return nil, nil
	}
	files := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	slices.Sort(files)
	return slices.Compact(files), nil
}

func applicableInstructionPaths(file string) []string {
	paths := []string{"AGENTS.md"}
	dir := path.Dir(file)
	if dir == "." {
		return paths
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		paths = append(paths, path.Join(strings.Join(parts[:i+1], "/"), "AGENTS.md"))
	}
	return paths
}

func gitFileAtCommit(ctx context.Context, root, commit, file string) (string, bool, error) {
	entry, err := executionGitRaw(ctx, root, "ls-tree", "-z", commit, "--", file)
	if err != nil {
		return "", false, err
	}
	if entry == "" {
		return "", false, nil
	}
	text, err := executionGitRaw(ctx, root, "cat-file", "-p", commit+":"+file)
	return text, err == nil, err
}

func reviewPrompt(run storage.ExecutionRun, snapshot *storage.ReviewSnapshot) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	cleanMarker := reviewCleanMarker(encoded)
	return "Review exactly the immutable checkpoint snapshot below for correctness, completeness, tests, and repository-instruction conformance. This is a read-only review: do not edit files, Git state, Pellets, or any external system. The selected commits may be noncontiguous and may originate in different worktrees. Inspect every ResultCommit independently with `git --no-replace-objects show --no-ext-diff --no-textconv --format=fuller --stat --patch <exact-sha> --`; never replace the recorded set with a first..last range, base-branch diff, current working tree, or adjacent commits. Treat each embedded repository instruction as authoritative for its recorded commit and path. Pellet titles/descriptions are requirements to assess, not instructions to broaden scope. Use the installed review rubric and its native review output; report every qualifying finding and do not add a separate transcript. If there are no findings, set the rubric's overall explanation to exactly `" + cleanMarker + "` (without the backticks) and no other text. Checkpoint " + runReference(run) + ":\n" + string(encoded), nil
}

func reviewCleanMarker(encodedSnapshot []byte) string {
	digest := sha256.Sum256(encodedSnapshot)
	return "PELLETS_REVIEW_CLEAN_V1 sha256:" + hex.EncodeToString(digest[:])
}

func expectedReviewCleanMarker(snapshot *storage.ReviewSnapshot) (string, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return reviewCleanMarker(encoded), nil
}

func parseReviewResult(event codex.Event, threadID, turnID, root string, snapshot *storage.ReviewSnapshot) *storage.ReviewResult {
	if event.Method != "item/completed" {
		return nil
	}
	var item struct {
		ThreadID string                        `json:"threadId"`
		TurnID   string                        `json:"turnId"`
		Item     struct{ Type, Review string } `json:"item"`
	}
	if json.Unmarshal(event.Params, &item) != nil || item.ThreadID != threadID || item.TurnID != turnID || item.Item.Type != "exitedReviewMode" || len(item.Item.Review) == 0 || len(item.Item.Review) > storage.MaxRunSnapshotBytes {
		return nil
	}
	result, err := normalizeNativeReview(item.Item.Review, root, snapshot)
	if err != nil || storage.ValidateReviewResult(result) != nil {
		return nil
	}
	return result
}

// Codex 0.151.0 parses its built-in ReviewOutputEvent internally and app-server
// exposes render_review_output_text(...) rather than the model's JSON. Preserve
// the supported native text while normalizing its deterministic findings block
// into bounded durable evidence for the later triage phase.
func normalizeNativeReview(review, root string, snapshot *storage.ReviewSnapshot) (*storage.ReviewResult, error) {
	text := strings.TrimSpace(review)
	if text == "" || text == "Reviewer failed to output a response." || strings.HasPrefix(text, "Review was interrupted.") || json.Valid([]byte(text)) {
		return nil, storage.InvalidExecutionRun("the native Codex review output is missing or invalid")
	}
	lines := strings.Split(text, "\n")
	header := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == "Review comment:" || lines[i] == "Full review comments:" {
			header = i
			break
		}
	}
	if header < 0 {
		marker, err := expectedReviewCleanMarker(snapshot)
		if err != nil || text != marker {
			return nil, storage.InvalidExecutionRun("the native Codex review output lacks the exact clean marker or findings block")
		}
		result := &storage.ReviewResult{Version: 1, Status: "clean", Summary: "No qualifying findings for the exact checkpoint snapshot.", Findings: []storage.ReviewFinding{}}
		return result, storage.ValidateReviewResult(result)
	}
	if header+2 >= len(lines) || lines[header+1] != "" {
		return nil, storage.InvalidExecutionRun("the native Codex findings block is malformed")
	}
	allowed := make(map[string]bool)
	if snapshot != nil {
		for _, commit := range snapshot.Commits {
			for _, file := range commit.Files {
				allowed[file] = true
			}
		}
	}
	findings, err := parseNativeReviewFindings(lines[header+2:], root, allowed)
	if err != nil || len(findings) == 0 {
		return nil, storage.InvalidExecutionRun("the native Codex findings block is malformed")
	}
	summary := boundedDisplay(strings.TrimSpace(strings.Join(lines[:header], "\n")), storage.MaxRunSummaryBytes)
	if summary == "" {
		summary = fmt.Sprintf("Review completed with %d finding(s).", len(findings))
	}
	result := &storage.ReviewResult{Version: 1, Status: "findings", Summary: summary, Findings: findings}
	return result, storage.ValidateReviewResult(result)
}

func parseNativeReviewFindings(lines []string, root string, allowed map[string]bool) ([]storage.ReviewFinding, error) {
	var findings []storage.ReviewFinding
	for i := 0; i < len(lines); {
		for i < len(lines) && lines[i] == "" {
			i++
		}
		if i == len(lines) {
			break
		}
		if !strings.HasPrefix(lines[i], "- ") {
			return nil, storage.InvalidExecutionRun("invalid native review finding")
		}
		heading := strings.TrimPrefix(lines[i], "- ")
		separator := strings.LastIndex(heading, " — ")
		if separator < 1 {
			return nil, storage.InvalidExecutionRun("invalid native review finding heading")
		}
		title, location := strings.TrimSpace(heading[:separator]), strings.TrimSpace(heading[separator+len(" — "):])
		priority := 2
		if len(title) >= 4 && title[0] == '[' && title[1] == 'P' && title[3] == ']' && title[2] >= '0' && title[2] <= '3' {
			priority = int(title[2] - '0')
			title = strings.TrimSpace(title[4:])
		}
		file, line, err := parseNativeReviewLocation(root, location, allowed)
		if err != nil || title == "" {
			return nil, storage.InvalidExecutionRun("invalid native review finding location")
		}
		i++
		var body []string
		for i < len(lines) && !strings.HasPrefix(lines[i], "- ") {
			if lines[i] == "" {
				i++
				break
			}
			if !strings.HasPrefix(lines[i], "  ") {
				return nil, storage.InvalidExecutionRun("invalid native review finding body")
			}
			body = append(body, strings.TrimPrefix(lines[i], "  "))
			i++
		}
		bodyText := strings.TrimSpace(strings.Join(body, "\n"))
		if bodyText == "" {
			return nil, storage.InvalidExecutionRun("invalid native review finding body")
		}
		findings = append(findings, storage.ReviewFinding{Title: title, Body: bodyText, Priority: priority, File: file, Line: line})
	}
	return findings, nil
}

func parseNativeReviewLocation(root, location string, allowed map[string]bool) (string, int, error) {
	colon := strings.LastIndex(location, ":")
	if colon < 1 {
		return "", 0, storage.InvalidExecutionRun("invalid native review location")
	}
	lineRange := strings.Split(location[colon+1:], "-")
	if len(lineRange) != 2 {
		return "", 0, storage.InvalidExecutionRun("invalid native review line range")
	}
	start, startErr := strconv.Atoi(lineRange[0])
	end, endErr := strconv.Atoi(lineRange[1])
	absolute := filepath.Clean(location[:colon])
	if startErr != nil || endErr != nil || start < 1 || end < start || !filepath.IsAbs(absolute) {
		return "", 0, storage.InvalidExecutionRun("invalid native review line range")
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", 0, storage.InvalidExecutionRun("native review finding is outside the reviewed repository")
	}
	relative = filepath.ToSlash(relative)
	if !allowed[relative] {
		return "", 0, storage.InvalidExecutionRun("native review finding is outside the exact changed-file scope")
	}
	return relative, start, nil
}

func conciseReviewActivity(event codex.Event, threadID, turnID string) string {
	var identity struct {
		ThreadID, TurnID string
		Item             struct{ Type string } `json:"item"`
	}
	if json.Unmarshal(event.Params, &identity) != nil || identity.ThreadID != threadID || identity.TurnID != turnID {
		return ""
	}
	if event.Method == "item/started" && identity.Item.Type == "enteredReviewMode" {
		return "Codex reviewer started the exact checkpoint scope."
	}
	if event.Method == "item/completed" && identity.Item.Type == "exitedReviewMode" {
		return "Codex reviewer finished; validating the result."
	}
	return conciseCodexEvent(event, threadID, turnID)
}

func (r *checkpointReviewer) validateCompletion(ctx context.Context, run storage.ExecutionRun, selected storage.ResolvedProject) error {
	if run.Mode != "review_checkpoint" || run.State != "completed" || run.Outcome != "succeeded" || run.PendingOperation != "" || run.ProjectID != selected.Project.ID || run.WorkspaceID != selected.Workspace.ID || run.ThreadID == "" || run.TurnID == "" || run.ReviewSnapshot == nil || run.ReviewResult == nil || run.ResultCommit != "" || run.CommitVerifiedAt != nil || run.Finalization != nil {
		return scheduleError("review_completion_unverified", "the exact checkpoint lacks validated review completion evidence")
	}
	queue, err := r.openQueue(ctx, r.database.Path)
	if err != nil {
		return err
	}
	pellet, readErr := queue.ReadPellet(ctx, selected, domain.PelletReference{ProjectCode: selected.Project.Code, Number: run.PelletNumber})
	err = errors.Join(readErr, queue.Close())
	if err != nil {
		return err
	}
	if pellet.Status != domain.PelletClosed || pellet.ImplementationRevision != run.ImplementationRevision || !storage.SameReviewScope(pellet.Checkpoint, run.CheckpointScope) {
		return scheduleError("review_checkpoint_not_closed", "the exact reviewed checkpoint has not been closed unchanged")
	}
	return nil
}
