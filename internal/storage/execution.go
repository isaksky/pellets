package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"pellets/internal/domain"
)

const (
	MaxRunActivity       = 64
	MaxRunSummaryBytes   = 1024
	MaxRunSnapshotBytes  = 1 << 20
	MaxInteractionBytes  = 256 << 10
	MaxPromptPrefixBytes = 2 << 20
	// This matches the SQLite prompt_prefix_json CHECK. Text can require JSON
	// escaping, so validation below measures the encoded record rather than
	// assuming its raw byte length is sufficient.
	MaxPromptPrefixJSONBytes = 2 << 20
)

// PromptPrefix is the immutable, normalized Pellets layer supplied to a new
// Codex conversation. It deliberately records the exact snapshot used for an
// attempt without copying Codex configuration, AGENTS.md, or a transcript.
// Those remain owned by the installed runtime.
type PromptPrefix struct {
	TemplateVersion string `json:"template_version"`
	SkillSHA256     string `json:"skill_sha256"`
	HelpSHA256      string `json:"help_sha256"`
	ToolExecutable  string `json:"tool_executable"`
	ToolVersion     string `json:"tool_version"`
	Text            string `json:"text"`
}

// CodexRuntimeEvidence is machine-local provenance, never runtime selection.
// Old run snapshots may lack it; their original executable remains historical.
type CodexRuntimeEvidence struct {
	Executable string `json:"executable"`
	Version    string `json:"version"`
	Managed    bool   `json:"managed"`
}

// EffectiveRunSettings is an allowlist, never a copy of app-server config,
// request payloads, environment, account data, or transcript content.
type EffectiveRunSettings struct {
	Runtime             CodexRuntimeEvidence `json:"runtime,omitempty"`
	Codex               CodexRunSettings     `json:"codex"`
	ApprovalPolicy      string               `json:"approval_policy"`
	ApprovalsReviewer   string               `json:"approvals_reviewer"`
	SandboxMode         string               `json:"sandbox_mode"`
	WritableRoots       []string             `json:"writable_roots,omitempty"`
	NetworkAccess       bool                 `json:"network_access"`
	ExcludeSlashTmp     bool                 `json:"exclude_slash_tmp"`
	ExcludeTmpdirEnvVar bool                 `json:"exclude_tmpdir_env_var"`
}

// RunCapture is immutable for an attempt. Nil filters mean unfiltered; supplied
// values retain exact bytes. An explicit resume creates a new numbered attempt.
type RunCapture struct {
	FreshConversation bool `json:"fresh_conversation,omitempty"`
	// Admission-only guards, checked in the creation transaction. Zero/nil
	// preserve callers without a preflight snapshot. Neither is persisted.
	ExpectedImplementationRevision int64                `json:"expected_implementation_revision,omitempty"`
	ExpectedWorkspace              *ResolvedProject     `json:"-"`
	ProjectID                      int64                `json:"project_id"`
	WorkspaceID                    int64                `json:"workspace_id"`
	PelletNumber                   int64                `json:"pellet_number"`
	ResumeFrom                     *int64               `json:"resume_from,omitempty"`
	Mode                           string               `json:"mode"`
	ExternalID                     *string              `json:"external_id"`
	Group                          *string              `json:"group"`
	Settings                       EffectiveRunSettings `json:"settings"`
	StartingHead                   string               `json:"starting_head"`
	StartingRef                    string               `json:"starting_ref"`
	ScheduleMode                   string               `json:"schedule_mode"`
	ScheduleRemaining              int                  `json:"schedule_remaining"`
	PromptPrefix                   PromptPrefix         `json:"prompt_prefix"`
}

// RunMatchesPelletGeneration conservatively treats unknown legacy generations
// as matching: absence of revision evidence cannot authorize a new conversation.
func RunMatchesPelletGeneration(run ExecutionRun, pellet Pellet) bool {
	return run.ProjectID == pellet.ProjectID && run.PelletNumber == pellet.Reference.Number &&
		(run.ImplementationRevision == 0 || run.ImplementationRevision == pellet.ImplementationRevision)
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
	// CachedInputTokens is present only when the installed runtime reports it.
	// Pellets records telemetry; it never promises a provider cache hit.
	CachedInputTokens *int64 `json:"cached_input_tokens,omitempty"`
	// Finalization is immutable evidence captured after the implementation
	// turn succeeds and before staging/commit. It survives explicit resumes.
	Finalization *FinalizationEvidence `json:"finalization,omitempty"`
	// Interaction is the one outstanding app-server request, if any. It keeps
	// only the bounded fields needed to render and correlate a response; answers,
	// credentials, command text, and transcript content are never retained.
	Interaction *RunInteraction `json:"interaction,omitempty"`
	// ReviewSnapshot freezes the exact checkpoint inputs before any reviewer
	// conversation exists. ReviewResult is the bounded, structured result from
	// that conversation. Both are immutable once written.
	ReviewSnapshot *ReviewSnapshot `json:"review_snapshot,omitempty"`
	ReviewResult   *ReviewResult   `json:"review_result,omitempty"`
}

type ReviewSnapshot struct {
	Version                int                 `json:"version"`
	RepositoryHead         string              `json:"repository_head"`
	RepositoryStatusSHA256 string              `json:"repository_status_sha256"`
	RepositoryRefsSHA256   string              `json:"repository_refs_sha256"`
	RepositoryRefContext   *ReviewRefContext   `json:"repository_ref_context,omitempty"`
	Targets                []ReviewTarget      `json:"targets"`
	Commits                []ReviewCommit      `json:"commits"`
	Instructions           []ReviewInstruction `json:"instructions"`
}

// ReviewRefContext attributes ordinary branch advances to another worktree.
// Older snapshots lack this evidence and retain the strict all-refs check.
type ReviewRefContext struct {
	HeadReference string              `json:"head_reference"`
	Worktrees     []ReviewWorktreeRef `json:"worktrees"`
}

type ReviewWorktreeRef struct {
	Root          string `json:"root"`
	Branch        string `json:"branch"`
	Head          string `json:"head"`
	HeadLogBytes  int64  `json:"head_log_bytes"`
	HeadLogSHA256 string `json:"head_log_sha256"`
}

type ReviewCommit struct {
	Reference    string   `json:"reference"`
	WorkspaceID  int64    `json:"workspace_id"`
	StartingHead string   `json:"starting_head"`
	ResultCommit string   `json:"result_commit"`
	Files        []string `json:"files"`
}

type ReviewInstruction struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Text   string `json:"text"`
}

type ReviewResult struct {
	Version  int             `json:"version"`
	Status   string          `json:"status"`
	Summary  string          `json:"summary"`
	Findings []ReviewFinding `json:"findings"`
}

type ReviewFinding struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Priority int    `json:"priority"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

type RunInteraction struct {
	RequestID string                `json:"request_id"`
	Method    string                `json:"method"`
	ThreadID  string                `json:"thread_id"`
	TurnID    string                `json:"turn_id,omitempty"`
	ItemID    string                `json:"item_id,omitempty"`
	Title     string                `json:"title"`
	Detail    string                `json:"detail,omitempty"`
	Mode      string                `json:"mode,omitempty"`
	Questions []InteractionQuestion `json:"questions,omitempty"`
	// RequestedPermissions is retained only for the exact turn-scoped grant
	// response. It is not rendered and must contain no credentials.
	RequestedPermissions json.RawMessage `json:"requested_permissions,omitempty"`
}

type InteractionQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header"`
	Question string              `json:"question"`
	Options  []InteractionOption `json:"options,omitempty"`
	Other    bool                `json:"other,omitempty"`
	Secret   bool                `json:"secret,omitempty"`
	Type     string              `json:"type,omitempty"`
	Required bool                `json:"required,omitempty"`
}

type InteractionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type FinalizationEvidence struct {
	NoChanges bool     `json:"no_changes,omitempty"`
	Files     []string `json:"files"`
	Tree      string   `json:"tree"`
	Subject   string   `json:"subject"`
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
	ImplementationRevision int64             `json:"implementation_revision"`
	CheckpointScope        *ReviewCheckpoint `json:"checkpoint_scope,omitempty"`
	CheckpointTriage       *CheckpointTriage `json:"checkpoint_triage,omitempty"`
	ID                     int64             `json:"id"`
	Attempt                int64             `json:"attempt"`
	Revision               int64             `json:"revision"`
	PendingOperation       string            `json:"pending_operation,omitempty"`
	PendingRevision        int64             `json:"pending_revision,omitempty"`
	PendingTurnID          string            `json:"-"`
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
	CheckpointTriageDatabase
	CreateExecutionRun(context.Context, RunCapture) (ExecutionRun, error)
	ReadExecutionRun(context.Context, int64) (ExecutionRun, error)
	ListWorkspaceRuns(context.Context, int64, int64, int) ([]ExecutionRun, error)
	UpdateExecutionRun(context.Context, UpdateExecutionRun) (ExecutionRun, error)
	InterruptExecutionRun(context.Context, int64, int64, string) (ExecutionRun, error)
	BeginExecutionOperation(context.Context, int64, int64, string) (ExecutionRun, error)
	FinishExecutionOperation(context.Context, ExecutionOperationResult) (ExecutionRun, error)
	CompleteReviewCheckpoint(context.Context, int64, int64) (ExecutionRun, error)
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
	Summary         string
}

var fullCommitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var (
	safeRunID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)
	hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func IsFullCommitID(value string) bool { return fullCommitID.MatchString(value) }

func ValidateRunCapture(c RunCapture) error {
	if len(c.StartingRef) > 4096 || strings.ContainsAny(c.StartingRef, "\x00\r\n") || !utf8.ValidString(c.StartingRef) {
		return InvalidExecutionRun("invalid starting Git reference")
	}
	if c.ScheduleMode != "" && c.ScheduleMode != "run_one" && c.ScheduleMode != "drain" && c.ScheduleMode != "watch" || c.ScheduleRemaining < 0 || c.ScheduleRemaining > 10000 {
		return InvalidExecutionRun("invalid saved schedule intent")
	}
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
	if err := ValidatePromptPrefix(c.PromptPrefix); err != nil {
		return err
	}
	s := c.Settings
	if s.ApprovalPolicy != "on-request" || s.ApprovalsReviewer != "auto_review" || s.SandboxMode != "workspace-write" {
		return InvalidExecutionRun("run settings must preserve automatic approval review and workspace-write")
	}
	for _, field := range []struct {
		value string
		bound int
	}{{s.Codex.Executable, 4096}, {s.Runtime.Executable, 4096}, {s.Runtime.Version, 128}, {s.Codex.Model, 512}, {s.Codex.ReasoningEffort, 128}} {
		if len(field.value) > field.bound || !utf8.ValidString(field.value) || strings.TrimSpace(field.value) != field.value || strings.ContainsAny(field.value, "\x00\r\n") {
			return InvalidExecutionRun("invalid effective Codex setting")
		}
	}
	l := s.Codex.Limits
	if l.MaxMessageBytes < 256 || l.MaxMessageBytes > 64<<20 || l.EventBuffer < 1 || l.EventBuffer > 65536 || l.MaxPending < 1 || l.MaxPending > 4096 || l.StderrBytes < 1 || l.StderrBytes > 1<<20 {
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

func ValidatePromptPrefix(prefix PromptPrefix) error {
	// Databases created before the prompt-prefix migration use this immutable
	// compatibility marker. New attempts never produce it.
	if prefix.TemplateVersion == "legacy" && prefix.SkillSHA256 == "" && prefix.HelpSHA256 == "" && prefix.ToolExecutable == "" && prefix.ToolVersion == "" && prefix.Text == "" {
		return nil
	}
	if prefix.TemplateVersion == "" || prefix.SkillSHA256 == "" || prefix.HelpSHA256 == "" || prefix.ToolExecutable == "" || prefix.ToolVersion == "" || prefix.Text == "" ||
		len(prefix.TemplateVersion) > 128 || len(prefix.SkillSHA256) != 64 || len(prefix.HelpSHA256) != 64 || len(prefix.ToolExecutable) > 4096 || len(prefix.ToolVersion) > 512 || len(prefix.Text) > MaxPromptPrefixBytes ||
		!utf8.ValidString(prefix.TemplateVersion) || !utf8.ValidString(prefix.ToolExecutable) || !utf8.ValidString(prefix.ToolVersion) || !utf8.ValidString(prefix.Text) || strings.ContainsRune(prefix.Text, 0) {
		return InvalidExecutionRun("invalid immutable Pellets prompt prefix")
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(prefix.SkillSHA256) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(prefix.HelpSHA256) {
		return InvalidExecutionRun("invalid immutable Pellets prompt prefix hash")
	}
	encoded, err := json.Marshal(prefix)
	if err != nil || len(encoded) > MaxPromptPrefixJSONBytes {
		return InvalidExecutionRun("immutable Pellets prompt prefix exceeds its encoded storage bound")
	}
	return nil
}

func ValidateRunProgress(p RunProgress) error {
	if err := ValidateRunInteraction(p.Interaction); err != nil {
		return err
	}
	if p.Interaction != nil && p.State != "awaiting_input" {
		return InvalidExecutionRun("a pending interaction requires awaiting-input state")
	}
	if p.Finalization != nil {
		f := p.Finalization
		if !IsFullCommitID(f.Tree) || (len(f.Files) == 0 && !f.NoChanges) || (f.NoChanges && len(f.Files) != 0) || len(f.Files) > 10000 || len(f.Subject) == 0 || len(f.Subject) > 240 || strings.ContainsAny(f.Subject, "\x00\r\n") {
			return InvalidExecutionRun("invalid finalization evidence")
		}
		for _, file := range f.Files {
			if file == "" || len(file) > 4096 || strings.ContainsRune(file, 0) || !utf8.ValidString(file) {
				return InvalidExecutionRun("invalid finalization file")
			}
		}
		encoded, _ := json.Marshal(f)
		if len(encoded) > MaxRunSnapshotBytes {
			return InvalidExecutionRun("finalization evidence exceeds storage bound")
		}
	}
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
	if p.CachedInputTokens != nil && *p.CachedInputTokens < 0 {
		return InvalidExecutionRun("cached input tokens must be nonnegative")
	}
	if err := ValidateReviewSnapshot(p.ReviewSnapshot); err != nil {
		return err
	}
	if err := ValidateReviewResult(p.ReviewResult); err != nil {
		return err
	}
	if p.ReviewResult != nil && p.ReviewSnapshot == nil {
		return InvalidExecutionRun("review result requires its immutable scope snapshot")
	}
	return nil
}

func ValidateReviewSnapshot(snapshot *ReviewSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.Version != 1 || !IsFullCommitID(snapshot.RepositoryHead) || !hexDigest.MatchString(snapshot.RepositoryStatusSHA256) || !hexDigest.MatchString(snapshot.RepositoryRefsSHA256) || len(snapshot.Targets) == 0 || len(snapshot.Targets) > 1000 || len(snapshot.Commits) != len(snapshot.Targets) {
		return InvalidExecutionRun("invalid immutable review snapshot")
	}
	if refs := snapshot.RepositoryRefContext; refs != nil {
		if refs.HeadReference != "HEAD" && !strings.HasPrefix(refs.HeadReference, "refs/heads/") {
			return InvalidExecutionRun("invalid immutable review HEAD reference")
		}
		seen := map[string]bool{}
		for _, worktree := range refs.Worktrees {
			if worktree.Root == "" || strings.ContainsRune(worktree.Root, 0) || !utf8.ValidString(worktree.Root) || !strings.HasPrefix(worktree.Branch, "refs/heads/") || strings.ContainsAny(worktree.Branch, "\x00\n") || worktree.Branch == refs.HeadReference || seen[worktree.Branch] || !IsFullCommitID(worktree.Head) || worktree.HeadLogBytes < 1 || !hexDigest.MatchString(worktree.HeadLogSHA256) {
				return InvalidExecutionRun("invalid immutable review worktree reference")
			}
			seen[worktree.Branch] = true
		}
	}
	resultCommits := make(map[string]bool, len(snapshot.Commits))
	for i, commit := range snapshot.Commits {
		target := snapshot.Targets[i]
		if target.Reason != "ready" || target.Status == nil || *target.Status != domain.PelletClosed || target.ImplementationRevision == nil || *target.ImplementationRevision < 1 || target.Evidence == nil ||
			commit.Reference == "" || commit.WorkspaceID < 1 || !IsFullCommitID(commit.StartingHead) || !IsFullCommitID(commit.ResultCommit) || (len(commit.Files) == 0 && commit.StartingHead != commit.ResultCommit) || len(commit.Files) > 10000 ||
			commit.Reference != target.Reference || commit.WorkspaceID != target.Evidence.WorkspaceID || commit.StartingHead != target.Evidence.StartingHead || commit.ResultCommit != target.Evidence.ResultCommit {
			return InvalidExecutionRun("invalid immutable review commit scope")
		}
		resultCommits[commit.ResultCommit] = true
		for j, file := range commit.Files {
			if file == "" || len(file) > 4096 || !utf8.ValidString(file) || strings.ContainsRune(file, 0) || j > 0 && commit.Files[j-1] >= file {
				return InvalidExecutionRun("invalid immutable review file scope")
			}
		}
	}
	seenInstructions := make(map[string]bool, len(snapshot.Instructions))
	for _, instruction := range snapshot.Instructions {
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(instruction.Text)))
		key := instruction.Commit + "\x00" + instruction.Path
		if !resultCommits[instruction.Commit] || instruction.Path == "" || len(instruction.Path) > 4096 || !hexDigest.MatchString(instruction.SHA256) || instruction.SHA256 != digest || seenInstructions[key] || !utf8.ValidString(instruction.Path+instruction.Text) || strings.ContainsRune(instruction.Path+instruction.Text, 0) {
			return InvalidExecutionRun("invalid immutable repository instruction snapshot")
		}
		seenInstructions[key] = true
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > MaxRunSnapshotBytes {
		return InvalidExecutionRun("immutable review snapshot exceeds its storage bound")
	}
	return nil
}

func ValidateReviewResult(result *ReviewResult) error {
	if result == nil {
		return nil
	}
	if result.Version != 1 || result.Status != "clean" && result.Status != "findings" || strings.TrimSpace(result.Summary) == "" || len(result.Findings) > 1000 {
		return InvalidExecutionRun("invalid structured review result")
	}
	if (result.Status == "clean") != (len(result.Findings) == 0) {
		return InvalidExecutionRun("review status does not match its findings")
	}
	for _, finding := range result.Findings {
		if strings.TrimSpace(finding.Title) == "" || strings.TrimSpace(finding.Body) == "" || finding.Priority < 0 || finding.Priority > 3 || finding.Line < 0 || len(finding.File) > 4096 || !utf8.ValidString(finding.Title+finding.Body+finding.File) || strings.ContainsRune(finding.Title+finding.Body+finding.File, 0) {
			return InvalidExecutionRun("invalid structured review finding")
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > MaxRunSnapshotBytes {
		return InvalidExecutionRun("structured review result exceeds its storage bound")
	}
	return nil
}

func ValidateRunInteraction(interaction *RunInteraction) error {
	if interaction == nil {
		return nil
	}
	if interaction.RequestID == "" || len(interaction.RequestID) > 512 || interaction.ThreadID == "" || !safeRunID.MatchString(interaction.ThreadID) ||
		(interaction.TurnID != "" && !safeRunID.MatchString(interaction.TurnID)) || len(interaction.ItemID) > 256 || !utf8.ValidString(interaction.RequestID) || !utf8.ValidString(interaction.ItemID) ||
		len(interaction.Title) == 0 || len(interaction.Title) > 1024 || len(interaction.Detail) > 4096 || !utf8.ValidString(interaction.Title) || !utf8.ValidString(interaction.Detail) ||
		strings.ContainsRune(interaction.Title, 0) || strings.ContainsRune(interaction.Detail, 0) {
		return InvalidExecutionRun("invalid pending interaction identity or display content")
	}
	switch interaction.Method {
	case "item/tool/requestUserInput":
		if interaction.TurnID == "" || len(interaction.Questions) == 0 || len(interaction.Questions) > 3 || len(interaction.RequestedPermissions) != 0 {
			return InvalidExecutionRun("invalid user-input interaction")
		}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if interaction.TurnID == "" || len(interaction.Questions) != 0 || len(interaction.RequestedPermissions) != 0 {
			return InvalidExecutionRun("invalid approval interaction")
		}
	case "item/permissions/requestApproval":
		if interaction.TurnID == "" || len(interaction.Questions) != 0 || len(interaction.RequestedPermissions) == 0 || !json.Valid(interaction.RequestedPermissions) {
			return InvalidExecutionRun("invalid permissions interaction")
		}
	case "mcpServer/elicitation/request":
		if interaction.ItemID != "" || (interaction.Mode != "form" && interaction.Mode != "url") || len(interaction.RequestedPermissions) != 0 || (interaction.Mode == "form" && len(interaction.Questions) == 0) || len(interaction.Questions) > 64 {
			return InvalidExecutionRun("invalid MCP elicitation interaction")
		}
	default:
		return InvalidExecutionRun("unsupported pending interaction method")
	}
	seen := map[string]bool{}
	for _, question := range interaction.Questions {
		if question.ID == "" || seen[question.ID] || len(question.ID) > 256 || len(question.Header) > 256 || question.Question == "" || len(question.Question) > 8192 ||
			!utf8.ValidString(question.ID+question.Header+question.Question) || strings.ContainsRune(question.ID+question.Header+question.Question, 0) || len(question.Options) > 32 {
			return InvalidExecutionRun("invalid pending interaction question")
		}
		seen[question.ID] = true
		if question.Type != "" && question.Type != "string" && question.Type != "number" && question.Type != "integer" && question.Type != "boolean" {
			return InvalidExecutionRun("unsupported pending interaction question type")
		}
		for _, option := range question.Options {
			if option.Label == "" || len(option.Label) > 1024 || len(option.Description) > 4096 || !utf8.ValidString(option.Label+option.Description) || strings.ContainsRune(option.Label+option.Description, 0) {
				return InvalidExecutionRun("invalid pending interaction option")
			}
		}
	}
	encoded, err := json.Marshal(interaction)
	if err != nil || len(encoded) > MaxInteractionBytes {
		return InvalidExecutionRun("pending interaction exceeds its storage bound")
	}
	return nil
}

func RunActive(state string) bool { return state == "running" || state == "awaiting_input" }

// CompletedReviewReceipt identifies the durable result of the transaction that
// both completed a review attempt and closed its checkpoint. A supervisor may
// crash before cleaning its local execution lock, so this exact shape is also
// the authority for an explicit, idempotent closed-checkpoint reconciliation.
func CompletedReviewReceipt(run ExecutionRun) bool {
	return run.Mode == "review_checkpoint" && run.Phase == "review" && run.State == "completed" && run.Outcome == "succeeded" &&
		run.PendingOperation == "" && run.ThreadID != "" && run.TurnID != "" && run.CheckpointScope != nil && run.ReviewSnapshot != nil && run.ReviewResult != nil &&
		run.ResultCommit == "" && run.CommitVerifiedAt == nil && run.Finalization == nil
}

// ReviewReconciliationAttempt is a stopped descendant that retained all review
// evidence but did not itself complete the already-closed checkpoint receipt.
// Its ancestry must still be walked back to CompletedReviewReceipt before it
// can authorize another explicit Resume.
func ReviewReconciliationAttempt(run ExecutionRun) bool {
	return run.ResumeFrom != nil && run.Mode == "review_checkpoint" && run.Phase == "review" &&
		(run.State == "interrupted" || run.State == "needs_attention") && (run.Outcome == "failed" || run.Outcome == "cancelled" || run.Outcome == "unknown") &&
		run.PendingOperation == "" && run.ThreadID != "" && run.TurnID != "" && run.CheckpointScope != nil && run.ReviewSnapshot != nil && run.ReviewResult != nil &&
		run.ResultCommit == "" && run.CommitVerifiedAt == nil && run.Finalization == nil
}

// SameReviewReconciliationEvidence proves that child is the next attempt in
// parent's exact review-reconciliation lineage. Effective runtime settings may
// be re-resolved between attempts, but the scope, conversation and result may
// not change.
func SameReviewReconciliationEvidence(child, parent ExecutionRun) bool {
	return child.ResumeFrom != nil && *child.ResumeFrom == parent.ID && child.ID > parent.ID && child.Attempt == parent.Attempt+1 &&
		child.ProjectID == parent.ProjectID && child.WorkspaceID == parent.WorkspaceID && child.PelletNumber == parent.PelletNumber && child.ProjectCode == parent.ProjectCode &&
		child.ImplementationRevision == parent.ImplementationRevision && child.StartingHead == parent.StartingHead && child.StartingRef == parent.StartingRef &&
		child.PelletTitle == parent.PelletTitle && child.PelletDescription == parent.PelletDescription && child.ThreadID == parent.ThreadID && child.TurnID == parent.TurnID &&
		child.Mode == parent.Mode && child.ScheduleMode == parent.ScheduleMode && child.ScheduleRemaining == parent.ScheduleRemaining && child.PelletPresent == parent.PelletPresent &&
		reflect.DeepEqual(child.ExternalID, parent.ExternalID) && reflect.DeepEqual(child.Group, parent.Group) && reflect.DeepEqual(child.PromptPrefix, parent.PromptPrefix) &&
		SameReviewScope(child.CheckpointScope, parent.CheckpointScope) && reflect.DeepEqual(child.ReviewSnapshot, parent.ReviewSnapshot) && reflect.DeepEqual(child.ReviewResult, parent.ReviewResult) &&
		child.ResultCommit == "" && child.CommitVerifiedAt == nil && child.Finalization == nil
}

func InvalidExecutionRun(message string) error {
	return domain.NewError(domain.Usage, "invalid_execution_run", message, nil)
}
func ExecutionRunConflict(id int64) error {
	return domain.NewError(domain.Conflict, "execution_run_conflict", "run evidence changed or the requested transition is no longer valid; reload before continuing", map[string]any{"run_id": id})
}
func ExecutionRunNotFound(id int64) error {
	return domain.NewError(domain.NotFound, "execution_run_not_found", fmt.Sprintf("execution run %d is unavailable; do not substitute another target", id), map[string]any{"run_id": id})
}

// ResumeUsesCurrentHead applies only before ordinary commit finalization.
// Existing unfinished work is preserved when replacing an old baseline.
func ResumeUsesCurrentHead(run ExecutionRun) bool {
	if run.Mode == "review_checkpoint" || run.Finalization != nil || run.ResultCommit != "" {
		return false
	}
	switch run.Phase {
	case "commit", "close", "finalization", "review":
		return false
	}
	return true
}
