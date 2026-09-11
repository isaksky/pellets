package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"pellets/internal/storage"
)

const (
	RecommendedModel           = "gpt-5.6-sol"
	RecommendedReasoningEffort = "high"

	defaultMaxMessageBytes  = 8 << 20
	defaultEventBuffer      = 256
	defaultMaxPending       = 128
	defaultStderrBytes      = 32 << 10
	maximumMaxMessageBytes  = 64 << 20
	maximumEventBuffer      = 65536
	maximumMaxPending       = 4096
	maximumStderrBytes      = 1 << 20
	maxModelPages           = 32
	maxModels               = 4096
	pelletsToolProbeTimeout = 5 * time.Second
	pelletsToolWaitTimeout  = 2 * time.Second
)

var (
	ErrUnauthenticated   = errors.New("Codex is not signed in")
	ErrPolicyUnavailable = errors.New("Codex automatic approval review is unavailable")
	ErrToolUnavailable   = errors.New("required Pellets tool is unavailable")
	ErrInvalidSettings   = errors.New("invalid Codex run settings")
)

// RunLimits are local transport bounds, not model token or account limits.
// Zero values select the adapter defaults.
type RunLimits = storage.CodexRunLimits

// WorkspaceRunSettings is the complete, credential-free value saved for one
// registered worktree. Empty model and effort preserve the installed Codex
// defaults; model identifiers are intentionally not enumerated by Pellets.
type WorkspaceRunSettings = storage.CodexRunSettings

// RunOverrides uses pointers so a single run can explicitly return a saved
// value to the installed default by supplying an empty string or zero.
type RunOverrides struct {
	Executable      *string            `json:"executable,omitempty"`
	Model           *string            `json:"model,omitempty"`
	ReasoningEffort *string            `json:"reasoning_effort,omitempty"`
	Limits          *RunLimitOverrides `json:"limits,omitempty"`
}

type RunLimitOverrides struct {
	MaxMessageBytes *int `json:"max_message_bytes,omitempty"`
	EventBuffer     *int `json:"event_buffer,omitempty"`
	MaxPending      *int `json:"max_pending,omitempty"`
	StderrBytes     *int `json:"stderr_bytes,omitempty"`
}

// RunSettings is the resolved value used for one app-server process.
type RunSettings struct {
	Executable      string    `json:"executable"`
	Model           string    `json:"model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Limits          RunLimits `json:"limits"`
}

// Machine-local environment selection never changes saved settings or their
// validation. A one-run override, including an empty reset, has final precedence.
func resolveLocalRunSettings(saved WorkspaceRunSettings, overrides RunOverrides) (RunSettings, error) {
	if overrides.Executable == nil {
		if executable, present := os.LookupEnv("PELLETS_CODEX_EXECUTABLE"); present {
			overrides.Executable = &executable
		}
	}
	return ResolveRunSettings(saved, overrides)
}

func RecommendedSettings() WorkspaceRunSettings {
	return WorkspaceRunSettings{Model: RecommendedModel, ReasoningEffort: RecommendedReasoningEffort}
}

func ResolveRunSettings(saved WorkspaceRunSettings, overrides RunOverrides) (RunSettings, error) {
	resolved := RunSettings{
		Executable:      saved.Executable,
		Model:           saved.Model,
		ReasoningEffort: saved.ReasoningEffort,
		Limits:          saved.Limits,
	}
	if overrides.Executable != nil {
		resolved.Executable = *overrides.Executable
	}
	if overrides.Model != nil {
		resolved.Model = *overrides.Model
	}
	if overrides.ReasoningEffort != nil {
		resolved.ReasoningEffort = *overrides.ReasoningEffort
	}
	if override := overrides.Limits; override != nil {
		if override.MaxMessageBytes != nil {
			resolved.Limits.MaxMessageBytes = *override.MaxMessageBytes
		}
		if override.EventBuffer != nil {
			resolved.Limits.EventBuffer = *override.EventBuffer
		}
		if override.MaxPending != nil {
			resolved.Limits.MaxPending = *override.MaxPending
		}
		if override.StderrBytes != nil {
			resolved.Limits.StderrBytes = *override.StderrBytes
		}
	}
	if err := validateSettingText("Codex executable", resolved.Executable, true); err != nil {
		return RunSettings{}, err
	}
	if err := validateSettingText("model", resolved.Model, true); err != nil {
		return RunSettings{}, err
	}
	if err := validateSettingText("reasoning effort", resolved.ReasoningEffort, true); err != nil {
		return RunSettings{}, err
	}
	limits := &resolved.Limits
	if limits.MaxMessageBytes == 0 {
		limits.MaxMessageBytes = defaultMaxMessageBytes
	}
	if limits.EventBuffer == 0 {
		limits.EventBuffer = defaultEventBuffer
	}
	if limits.MaxPending == 0 {
		limits.MaxPending = defaultMaxPending
	}
	if limits.StderrBytes == 0 {
		limits.StderrBytes = defaultStderrBytes
	}
	if limits.MaxMessageBytes < 256 || limits.EventBuffer < 1 || limits.MaxPending < 1 || limits.StderrBytes < 1 {
		return RunSettings{}, fmt.Errorf("%w: transport limits must be positive and max_message_bytes must be at least 256", ErrInvalidSettings)
	}
	if limits.MaxMessageBytes > maximumMaxMessageBytes || limits.EventBuffer > maximumEventBuffer || limits.MaxPending > maximumMaxPending || limits.StderrBytes > maximumStderrBytes {
		return RunSettings{}, fmt.Errorf("%w: transport limits exceed the supported bounded maximum", ErrInvalidSettings)
	}
	return resolved, nil
}

func validateSettingText(label, value string, optional bool) error {
	if value == "" && optional {
		return nil
	}
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("%w: %s must be non-empty UTF-8 without leading or trailing whitespace", ErrInvalidSettings, label)
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		return fmt.Errorf("%w: %s contains a control character", ErrInvalidSettings, label)
	}
	return nil
}

type PrepareOptions struct {
	WorkspaceDir  string
	DatabasePath  string
	ClientVersion string
	Saved         WorkspaceRunSettings
	Overrides     RunOverrides
}

type AccountStatus struct {
	Ready              bool   `json:"ready"`
	RequiresOpenAIAuth bool   `json:"requires_openai_auth"`
	Type               string `json:"type,omitempty"`
}

type ModelInfo struct {
	ID                        string   `json:"id"`
	Model                     string   `json:"model"`
	DisplayName               string   `json:"display_name"`
	Default                   bool     `json:"default"`
	SupportedReasoningEfforts []string `json:"supported_reasoning_efforts"`
}

// PreparedRun retains only non-secret preflight state. In particular it does
// not expose account email, credential material, or the installed raw config.
type PreparedRun struct {
	Client           *Client
	Settings         RunSettings
	Account          AccountStatus
	Models           []ModelInfo
	EvidenceSettings storage.EffectiveRunSettings
	PromptPrefix     storage.PromptPrefix
	thread           map[string]any
	turn             map[string]any
}

func (prepared *PreparedRun) ThreadStartParams() map[string]any {
	return cloneMap(prepared.thread)
}

func (prepared *PreparedRun) TurnStartParams(threadID string, input []any) (map[string]any, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, fmt.Errorf("%w: a thread ID is required", ErrInvalidSettings)
	}
	params := cloneMap(prepared.turn)
	params["threadId"] = threadID
	params["input"] = append([]any(nil), input...)
	return params, nil
}

func cloneMap(source map[string]any) map[string]any {
	data, _ := json.Marshal(source)
	var cloned map[string]any
	_ = json.Unmarshal(data, &cloned)
	return cloned
}

// PrepareRun starts the selected managed or overridden runtime and performs read-only
// account/config/model preflight. It never starts a thread or model turn.
func PrepareRun(ctx context.Context, options PrepareOptions) (_ *PreparedRun, err error) {
	settings, err := resolveLocalRunSettings(options.Saved, options.Overrides)
	if err != nil {
		return nil, err
	}
	workspace, database, err := resolveRunPaths(options.WorkspaceDir, options.DatabasePath)
	if err != nil {
		return nil, err
	}
	promptPrefix, err := preparePelletsPromptPrefix(ctx, workspace)
	if err != nil {
		return nil, err
	}
	client, err := Start(ctx, Config{
		Executable: settings.Executable, Dir: workspace, ClientVersion: options.ClientVersion,
		MaxMessageBytes: settings.Limits.MaxMessageBytes, EventBuffer: settings.Limits.EventBuffer,
		MaxPending: settings.Limits.MaxPending, StderrBytes: settings.Limits.StderrBytes,
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, client.Close())
		}
	}()

	account, err := readAccount(ctx, client)
	if err != nil {
		return nil, err
	}
	if !account.Ready {
		return nil, fmt.Errorf("%w: run %q login, then retry (using the same CODEX_HOME)", ErrUnauthenticated, client.Runtime().Executable)
	}
	requirements, err := readRequirements(ctx, client)
	if err != nil {
		return nil, err
	}
	if err := checkManagedPolicy(requirements); err != nil {
		return nil, err
	}
	effective, err := readEffectiveConfig(ctx, client, workspace)
	if err != nil {
		return nil, err
	}
	models, err := readModels(ctx, client)
	if err != nil {
		return nil, err
	}
	if err := validateEffort(settings, effective.Model, models); err != nil {
		return nil, err
	}

	writableRoots, addedRoot, err := writableRootsForDatabase(workspace, database, effective.Sandbox.WritableRoots)
	if err != nil {
		return nil, err
	}
	thread := map[string]any{
		"cwd": workspace, "approvalPolicy": "on-request", "approvalsReviewer": "auto_review", "sandbox": "workspace-write",
	}
	if settings.Model != "" {
		thread["model"] = settings.Model
	}
	if addedRoot {
		thread["config"] = map[string]any{"sandbox_workspace_write": map[string]any{"writable_roots": writableRoots}}
	}
	turn := map[string]any{
		"cwd": workspace, "approvalPolicy": "on-request", "approvalsReviewer": "auto_review",
		"sandboxPolicy": map[string]any{
			"type": "workspaceWrite", "writableRoots": writableRoots,
			"networkAccess":       effective.Sandbox.NetworkAccess,
			"excludeSlashTmp":     effective.Sandbox.ExcludeSlashTmp,
			"excludeTmpdirEnvVar": effective.Sandbox.ExcludeTmpdirEnvVar,
		},
	}
	if settings.Model != "" {
		turn["model"] = settings.Model
	}
	if settings.ReasoningEffort != "" {
		turn["effort"] = settings.ReasoningEffort
	}
	evidenceSettings := storage.EffectiveRunSettings{
		Runtime:        storage.CodexRuntimeEvidence{Executable: client.Runtime().Executable, Version: client.Runtime().Version, Managed: settings.Executable == ""},
		Codex:          storage.CodexRunSettings{Executable: settings.Executable, Model: settings.Model, ReasoningEffort: settings.ReasoningEffort, Limits: settings.Limits},
		ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write",
		WritableRoots: append([]string(nil), writableRoots...), NetworkAccess: effective.Sandbox.NetworkAccess,
		ExcludeSlashTmp: effective.Sandbox.ExcludeSlashTmp, ExcludeTmpdirEnvVar: effective.Sandbox.ExcludeTmpdirEnvVar,
	}
	if evidenceSettings.Codex.Model == "" {
		evidenceSettings.Codex.Model = effective.Model
	}
	if evidenceSettings.Codex.Model == "" {
		for _, model := range models {
			if model.Default {
				evidenceSettings.Codex.Model = model.Model
				break
			}
		}
	}
	if evidenceSettings.Codex.ReasoningEffort == "" {
		evidenceSettings.Codex.ReasoningEffort = effective.ReasoningEffort
	}
	return &PreparedRun{Client: client, Settings: settings, Account: account, Models: models, EvidenceSettings: evidenceSettings, PromptPrefix: promptPrefix, thread: thread, turn: turn}, nil
}

// requirePelletsTool checks the same cwd and inherited environment supplied to
// the Codex child. The bounded global version probe does not open or mutate a
// Pellets database, and its output is discarded rather than exposed or logged.
func requirePelletsTool(ctx context.Context, workspace string) error {
	return requirePelletsToolWithin(ctx, workspace, pelletsToolProbeTimeout)
}

func requirePelletsToolWithin(ctx context.Context, workspace string, timeout time.Duration) error {
	path, err := exec.LookPath("pl")
	if err != nil {
		return fmt.Errorf("%w: `pl` is not executable through the inherited PATH; install Pellets or add its executable directory to PATH, then retry", ErrToolUnavailable)
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.Command(path, "--version")
	command.Dir = workspace
	command.Env = os.Environ()
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	tree, err := startOwnedProcess(ctx, command)
	if err != nil {
		return errors.Join(
			fmt.Errorf("%w: could not start `pl --version` in the inherited child environment; reinstall Pellets or fix the executable, then retry", ErrToolUnavailable),
			err,
		)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case waitErr := <-done:
		cleanupErr := tree.terminate()
		if waitErr == nil && cleanupErr == nil {
			return nil
		}
		return errors.Join(
			fmt.Errorf("%w: `pl --version` failed in the inherited child environment; reinstall Pellets or fix the executable, then retry", ErrToolUnavailable),
			waitErr, cleanupErr,
		)
	case <-probeCtx.Done():
		cleanupErr := tree.terminate()
		var waitErr error
		select {
		case waitErr = <-done:
		case <-time.After(pelletsToolWaitTimeout):
			waitErr = errors.Join(ErrCleanup, errors.New("Pellets tool process did not finish after bounded process-tree cleanup"))
		}
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), cleanupErr, waitErr)
		}
		return errors.Join(
			fmt.Errorf("%w: `pl --version` timed out in the inherited child environment; reinstall Pellets or fix the executable, then retry", ErrToolUnavailable),
			cleanupErr, waitErr,
		)
	}
}

func resolveRunPaths(workspacePath, databasePath string) (string, string, error) {
	if workspacePath == "" || databasePath == "" {
		return "", "", fmt.Errorf("%w: workspace directory and bound Pellets database path are required", ErrInvalidSettings)
	}
	workspace, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve workspace: %v", ErrInvalidSettings, err)
	}
	workspaceInfo, err := os.Stat(workspace)
	if err != nil || !workspaceInfo.IsDir() {
		return "", "", fmt.Errorf("%w: workspace directory %q is unavailable", ErrInvalidSettings, workspace)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(workspace); evalErr == nil {
		workspace = evaluated
	}
	database, err := filepath.Abs(databasePath)
	if err != nil {
		return "", "", fmt.Errorf("%w: resolve bound Pellets database: %v", ErrInvalidSettings, err)
	}
	info, err := os.Stat(database)
	if err != nil || info.IsDir() {
		return "", "", fmt.Errorf("%w: bound Pellets database %q is unavailable", ErrInvalidSettings, database)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(database); evalErr == nil {
		database = evaluated
	}
	return filepath.Clean(workspace), filepath.Clean(database), nil
}

func readAccount(ctx context.Context, client Session) (AccountStatus, error) {
	result, err := client.Call(ctx, AccountRead, map[string]any{"refreshToken": false})
	if err != nil {
		return AccountStatus{}, fmt.Errorf("read Codex account status: %w", err)
	}
	var response struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return AccountStatus{}, fmt.Errorf("%w: invalid account/read response", ErrProtocol)
	}
	hasAccount := len(response.Account) != 0 && string(response.Account) != "null"
	status := AccountStatus{Ready: hasAccount || !response.RequiresOpenAIAuth, RequiresOpenAIAuth: response.RequiresOpenAIAuth}
	if hasAccount {
		var account struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(response.Account, &account) != nil || account.Type == "" {
			return AccountStatus{}, fmt.Errorf("%w: account/read returned an invalid account", ErrProtocol)
		}
		status.Type = account.Type
	}
	return status, nil
}

type managedRequirements struct {
	AllowedApprovalPolicies   []json.RawMessage `json:"allowedApprovalPolicies"`
	AllowedApprovalsReviewers []string          `json:"allowedApprovalsReviewers"`
	AllowedSandboxModes       []string          `json:"allowedSandboxModes"`
}

func readRequirements(ctx context.Context, client Session) (*managedRequirements, error) {
	result, err := client.Call(ctx, ConfigRequirementsRead, map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("read managed Codex requirements: %w", err)
	}
	var response struct {
		Requirements *managedRequirements `json:"requirements"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("%w: invalid configRequirements/read response", ErrProtocol)
	}
	return response.Requirements, nil
}

func checkManagedPolicy(requirements *managedRequirements) error {
	if requirements == nil {
		return nil
	}
	if requirements.AllowedApprovalPolicies != nil {
		allowed := false
		for _, policy := range requirements.AllowedApprovalPolicies {
			var value string
			if json.Unmarshal(policy, &value) == nil && value == "on-request" {
				allowed = true
			}
		}
		if !allowed {
			return fmt.Errorf("%w: managed Codex requirements do not allow approval_policy=on-request", ErrPolicyUnavailable)
		}
	}
	if requirements.AllowedApprovalsReviewers != nil && !slices.Contains(requirements.AllowedApprovalsReviewers, "auto_review") {
		return fmt.Errorf("%w: managed Codex requirements do not allow approvals_reviewer=auto_review", ErrPolicyUnavailable)
	}
	if requirements.AllowedSandboxModes != nil && !slices.Contains(requirements.AllowedSandboxModes, "workspace-write") {
		return fmt.Errorf("%w: managed Codex requirements do not allow sandbox_mode=workspace-write", ErrPolicyUnavailable)
	}
	return nil
}

type effectiveConfig struct {
	Model           string
	ReasoningEffort string
	Sandbox         struct {
		WritableRoots       []string
		NetworkAccess       bool
		ExcludeSlashTmp     bool
		ExcludeTmpdirEnvVar bool
	}
}

func readEffectiveConfig(ctx context.Context, client Session, workspace string) (effectiveConfig, error) {
	result, err := client.Call(ctx, ConfigRead, map[string]any{"cwd": workspace, "includeLayers": false})
	if err != nil {
		return effectiveConfig{}, fmt.Errorf("read effective Codex configuration: %w", err)
	}
	var response struct {
		Config *struct {
			Model                 string `json:"model"`
			ReasoningEffort       string `json:"model_reasoning_effort"`
			SandboxWorkspaceWrite *struct {
				WritableRoots       []string `json:"writable_roots"`
				NetworkAccess       bool     `json:"network_access"`
				ExcludeSlashTmp     bool     `json:"exclude_slash_tmp"`
				ExcludeTmpdirEnvVar bool     `json:"exclude_tmpdir_env_var"`
			} `json:"sandbox_workspace_write"`
		} `json:"config"`
	}
	if json.Unmarshal(result, &response) != nil || response.Config == nil {
		return effectiveConfig{}, fmt.Errorf("%w: invalid config/read response", ErrProtocol)
	}
	effective := effectiveConfig{Model: response.Config.Model, ReasoningEffort: response.Config.ReasoningEffort}
	if sandbox := response.Config.SandboxWorkspaceWrite; sandbox != nil {
		effective.Sandbox.WritableRoots = append([]string(nil), sandbox.WritableRoots...)
		effective.Sandbox.NetworkAccess = sandbox.NetworkAccess
		effective.Sandbox.ExcludeSlashTmp = sandbox.ExcludeSlashTmp
		effective.Sandbox.ExcludeTmpdirEnvVar = sandbox.ExcludeTmpdirEnvVar
	}
	return effective, nil
}

type modelListResponse struct {
	Data []struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		DisplayName string `json:"displayName"`
		IsDefault   bool   `json:"isDefault"`
		Efforts     []struct {
			Effort string `json:"reasoningEffort"`
		} `json:"supportedReasoningEfforts"`
	} `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

func readModels(ctx context.Context, client Session) ([]ModelInfo, error) {
	var models []ModelInfo
	var cursor *string
	seenCursors := make(map[string]bool)
	for page := 0; page < maxModelPages; page++ {
		params := map[string]any{"limit": 100}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		result, err := client.Call(ctx, ModelList, params)
		if err != nil {
			return nil, fmt.Errorf("read available Codex models: %w", err)
		}
		var response modelListResponse
		if err := json.Unmarshal(result, &response); err != nil {
			return nil, fmt.Errorf("%w: invalid model/list response", ErrProtocol)
		}
		for _, model := range response.Data {
			if model.ID == "" || model.Model == "" {
				return nil, fmt.Errorf("%w: model/list returned a model without an id or model name", ErrProtocol)
			}
			info := ModelInfo{ID: model.ID, Model: model.Model, DisplayName: model.DisplayName, Default: model.IsDefault}
			for _, option := range model.Efforts {
				if option.Effort == "" {
					return nil, fmt.Errorf("%w: model/list returned an empty reasoning effort", ErrProtocol)
				}
				info.SupportedReasoningEfforts = append(info.SupportedReasoningEfforts, option.Effort)
			}
			models = append(models, info)
			if len(models) > maxModels {
				return nil, fmt.Errorf("%w: model/list exceeded %d models", ErrBufferFull, maxModels)
			}
		}
		if response.NextCursor == nil {
			return models, nil
		}
		if *response.NextCursor == "" || seenCursors[*response.NextCursor] {
			return nil, fmt.Errorf("%w: model/list returned a missing or repeated cursor", ErrProtocol)
		}
		seenCursors[*response.NextCursor] = true
		cursor = response.NextCursor
	}
	return nil, fmt.Errorf("%w: model/list exceeded %d pages", ErrBufferFull, maxModelPages)
}

func validateEffort(settings RunSettings, configuredModel string, models []ModelInfo) error {
	if settings.ReasoningEffort == "" {
		return nil
	}
	modelName := settings.Model
	if modelName == "" {
		modelName = configuredModel
	}
	if modelName == "" {
		for _, model := range models {
			if model.Default {
				modelName = model.Model
				break
			}
		}
	}
	for _, model := range models {
		if model.Model != modelName && model.ID != modelName {
			continue
		}
		if slices.Contains(model.SupportedReasoningEfforts, settings.ReasoningEffort) {
			return nil
		}
		return fmt.Errorf("%w: reasoning effort %q is not supported by model %q; choose one advertised by the selected Codex runtime", ErrInvalidSettings, settings.ReasoningEffort, modelName)
	}
	return fmt.Errorf("%w: cannot validate reasoning effort %q because model %q is not in the selected Codex runtime catalog", ErrInvalidSettings, settings.ReasoningEffort, modelName)
}

func writableRootsForDatabase(workspace, database string, configured []string) ([]string, bool, error) {
	roots := make([]string, 0, len(configured)+1)
	for _, root := range configured {
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) {
			root = filepath.Join(workspace, root)
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, false, fmt.Errorf("%w: resolve configured Codex writable root: %v", ErrInvalidSettings, err)
		}
		absolute = filepath.Clean(absolute)
		if evaluated, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil {
			absolute = evaluated
		}
		if !slices.Contains(roots, absolute) {
			roots = append(roots, absolute)
		}
	}
	databaseDir := filepath.Dir(database)
	if pathWithin(workspace, databaseDir) {
		return roots, false, nil
	}
	for _, root := range roots {
		if pathWithin(root, databaseDir) {
			return roots, false, nil
		}
	}
	volume := filepath.VolumeName(databaseDir) + string(filepath.Separator)
	if filepath.Clean(databaseDir) == filepath.Clean(volume) {
		return nil, false, fmt.Errorf("%w: refusing to grant the filesystem root for a bound Pellets database", ErrInvalidSettings)
	}
	roots = append(roots, databaseDir)
	return roots, true, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
