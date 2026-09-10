package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"pellets/internal/domain"
)

const (
	MaxRunActivity      = 64
	MaxRunSummaryBytes  = 1024
	MaxRunSnapshotBytes = 1 << 20
)

// EffectiveRunSettings is an allowlist, never a copy of app-server config,
// request payloads, environment, account data, or transcript content.
type EffectiveRunSettings struct {
	Codex               CodexRunSettings `json:"codex"`
	ApprovalPolicy      string           `json:"approval_policy"`
	ApprovalsReviewer   string           `json:"approvals_reviewer"`
	SandboxMode         string           `json:"sandbox_mode"`
	WritableRoots       []string         `json:"writable_roots,omitempty"`
	NetworkAccess       bool             `json:"network_access"`
	ExcludeSlashTmp     bool             `json:"exclude_slash_tmp"`
	ExcludeTmpdirEnvVar bool             `json:"exclude_tmpdir_env_var"`
}

// RunCapture is immutable for an attempt. Nil filters mean unfiltered; supplied
// values retain exact bytes. An explicit resume creates a new numbered attempt.
type RunCapture struct {
	ProjectID    int64                `json:"project_id"`
	WorkspaceID  int64                `json:"workspace_id"`
	PelletNumber int64                `json:"pellet_number"`
	ResumeFrom   *int64               `json:"resume_from,omitempty"`
	Mode         string               `json:"mode"`
	ExternalID   *string              `json:"external_id"`
	Group        *string              `json:"group"`
	Settings     EffectiveRunSettings `json:"settings"`
	StartingHead string               `json:"starting_head"`
}

// RunProgress contains only orchestration evidence. Summary is application-
// authored concise text, never arbitrary Codex output. ErrorCode is a stable
// classification; raw error messages and stderr must not be stored here.
type RunProgress struct {
	Phase     string `json:"phase"`
	State     string `json:"state"`
	ThreadID  string `json:"thread_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

type RunActivity struct {
	Revision  int64     `json:"revision"`
	At        time.Time `json:"at"`
	Phase     string    `json:"phase"`
	State     string    `json:"state"`
	Summary   string    `json:"summary"`
	Operation string    `json:"operation,omitempty"`
}

type ExecutionRun struct {
	ID               int64  `json:"id"`
	Attempt          int64  `json:"attempt"`
	Revision         int64  `json:"revision"`
	PendingOperation string `json:"pending_operation,omitempty"`
	PendingRevision  int64  `json:"pending_revision,omitempty"`
	PendingTurnID    string `json:"-"`
	RunCapture
	RunProgress
	ProjectCode       string           `json:"project_code"`
	PelletTitle       string           `json:"pellet_title"`
	PelletDescription string           `json:"pellet_description"`
	PelletPresent     bool             `json:"pellet_present"`
	ResultCommit      string           `json:"result_commit,omitempty"`
	CommitVerifiedAt  *time.Time       `json:"commit_verified_at,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	FinishedAt        *time.Time       `json:"finished_at,omitempty"`
	ActivityPruned    bool             `json:"activity_pruned"`
	Activity          []RunActivity    `json:"activity"`
	WorkspaceRoot     domain.LocalPath `json:"-"`
	WorkspaceGitDir   domain.LocalPath `json:"-"`
	GitCommonDir      domain.LocalPath `json:"-"`
}

type UpdateExecutionRun struct {
	ID               int64
	ExpectedRevision int64
	Progress         RunProgress
	// Set only after local Git verifies this exact full object ID as a commit.
	// Once recorded, this evidence is immutable, including across retention.
	VerifiedCommit string
}

type ExecutionRunDatabase interface {
	CreateExecutionRun(context.Context, RunCapture) (ExecutionRun, error)
	ReadExecutionRun(context.Context, int64) (ExecutionRun, error)
	ListWorkspaceRuns(context.Context, int64, int64, int) ([]ExecutionRun, error)
	UpdateExecutionRun(context.Context, UpdateExecutionRun) (ExecutionRun, error)
	BeginExecutionOperation(context.Context, int64, int64, string) (ExecutionRun, error)
	FinishExecutionOperation(context.Context, ExecutionOperationResult) (ExecutionRun, error)
	PruneRunActivity(context.Context, time.Time, int) (int64, error)
	Close() error
}

// PendingRevision identifies one external call across unrelated progress
// revisions. It is not a process owner, lease, or automatic retry token.
type ExecutionOperationResult struct {
	ID              int64
	PendingRevision int64
	ThreadID        string
	TurnID          string
	ErrorCode       string
}

var fullCommitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var safeRunID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

func IsFullCommitID(value string) bool { return fullCommitID.MatchString(value) }

func ValidateRunCapture(c RunCapture) error {
	if c.ProjectID < 1 || c.WorkspaceID < 1 || c.PelletNumber < 1 || (c.ResumeFrom != nil && *c.ResumeFrom < 1) || !IsFullCommitID(c.StartingHead) {
		return InvalidExecutionRun("stable identities and a full starting commit ID are required")
	}
	switch c.Mode {
	case "run_one", "drain", "watch", "review_checkpoint":
	default:
		return InvalidExecutionRun("invalid captured run mode")
	}
	for _, filter := range []*string{c.ExternalID, c.Group} {
		if filter != nil && (*filter == "" || len(*filter) > 4096 || !utf8.ValidString(*filter)) {
			return InvalidExecutionRun("invalid exact run filter")
		}
	}
	s := c.Settings
	if s.ApprovalPolicy != "on-request" || s.ApprovalsReviewer != "auto_review" || s.SandboxMode != "workspace-write" {
		return InvalidExecutionRun("run settings must preserve automatic approval review and workspace-write")
	}
	for _, field := range []struct {
		value string
		bound int
	}{{s.Codex.Executable, 4096}, {s.Codex.Model, 512}, {s.Codex.ReasoningEffort, 128}} {
		if len(field.value) > field.bound || !utf8.ValidString(field.value) || strings.TrimSpace(field.value) != field.value || strings.ContainsAny(field.value, "\x00\r\n") {
			return InvalidExecutionRun("invalid effective Codex setting")
		}
	}
	l := s.Codex.Limits
	if s.Codex.Executable == "" || l.MaxMessageBytes < 256 || l.MaxMessageBytes > 64<<20 || l.EventBuffer < 1 || l.EventBuffer > 65536 || l.MaxPending < 1 || l.MaxPending > 4096 || l.StderrBytes < 1 || l.StderrBytes > 1<<20 {
		return InvalidExecutionRun("effective transport settings must be resolved and bounded")
	}
	if len(s.WritableRoots) > 64 {
		return InvalidExecutionRun("too many writable roots")
	}
	for _, root := range s.WritableRoots {
		if root == "" || len(root) > 4096 || !utf8.ValidString(root) || strings.ContainsRune(root, 0) {
			return InvalidExecutionRun("invalid writable root")
		}
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > 32768 {
		return InvalidExecutionRun("effective settings exceed the storage limit")
	}
	return nil
}

func ValidateRunProgress(p RunProgress) error {
	// Phases describe durable intent immediately before the corresponding
	// external operation. New phases require an explicit schema migration.
	switch p.Phase {
	case "preflight", "thread_start", "turn_start", "implementation", "verification", "commit", "close", "review", "finalization":
	default:
		return InvalidExecutionRun("invalid run phase")
	}
	switch p.State {
	case "running", "awaiting_input", "interrupted", "needs_attention", "completed":
	default:
		return InvalidExecutionRun("invalid run state")
	}
	if RunActive(p.State) {
		if p.Outcome != "" {
			return InvalidExecutionRun("active run cannot have a terminal outcome")
		}
	} else {
		switch p.Outcome {
		case "succeeded", "failed", "cancelled", "unknown":
		default:
			return InvalidExecutionRun("stopped run requires an explicit outcome")
		}
		if (p.State == "completed") != (p.Outcome == "succeeded") {
			return InvalidExecutionRun("only completed runs may succeed")
		}
	}
	for _, id := range []string{p.ThreadID, p.TurnID, p.ErrorCode} {
		if id != "" && !safeRunID.MatchString(id) {
			return InvalidExecutionRun("invalid conversation ID or error code")
		}
	}
	if p.TurnID != "" && p.ThreadID == "" {
		return InvalidExecutionRun("turn ID requires a thread ID")
	}
	if len(p.Summary) > MaxRunSummaryBytes || !utf8.ValidString(p.Summary) || strings.ContainsRune(p.Summary, 0) {
		return InvalidExecutionRun("activity summary exceeds its UTF-8 storage bound")
	}
	return nil
}

func RunActive(state string) bool { return state == "running" || state == "awaiting_input" }

func InvalidExecutionRun(message string) error {
	return domain.NewError(domain.Usage, "invalid_execution_run", message, nil)
}
func ExecutionRunConflict(id int64) error {
	return domain.NewError(domain.Conflict, "execution_run_conflict", "run evidence changed or the requested transition is no longer valid; reload before continuing", map[string]any{"run_id": id})
}
func ExecutionRunNotFound(id int64) error {
	return domain.NewError(domain.NotFound, "execution_run_not_found", fmt.Sprintf("execution run %d is unavailable; do not substitute another target", id), map[string]any{"run_id": id})
}
