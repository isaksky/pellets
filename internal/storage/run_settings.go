package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// CodexRunLimits are local app-server transport bounds. Zero selects the
// adapter default and is retained as zero when settings are saved.
type CodexRunLimits struct {
	MaxMessageBytes int `json:"max_message_bytes,omitempty"`
	EventBuffer     int `json:"event_buffer,omitempty"`
	MaxPending      int `json:"max_pending,omitempty"`
	StderrBytes     int `json:"stderr_bytes,omitempty"`
}

// CodexRunSettings is deliberately credential-free. Empty model and effort
// retain the installed runtime's normal defaults.
type CodexRunSettings struct {
	Executable      string         `json:"executable,omitempty"`
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Limits          CodexRunLimits `json:"limits,omitempty"`
}

// SavedWorkspaceRunSettings belongs to the stable workspace row, not to a
// project code, checkout path, account, or Codex credential.
type SavedWorkspaceRunSettings struct {
	WorkspaceID int64            `json:"workspace_id"`
	Settings    CodexRunSettings `json:"settings"`
	Version     string           `json:"version,omitempty"`
	Revision    int64            `json:"-"`
	CreatedAt   time.Time        `json:"created_at,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at,omitempty"`
}

type SaveWorkspaceRunSettingsRequest struct {
	WorkspaceID     int64
	Settings        CodexRunSettings
	ExpectedVersion string
}

// WorkspaceRunSettingsVersion is an opaque digest of the complete durable row,
// including its monotonic revision. A workspace with no saved row has no token.
func WorkspaceRunSettingsVersion(settings SavedWorkspaceRunSettings) string {
	if settings.Revision == 0 {
		return ""
	}
	encoded, err := json.Marshal(struct {
		WorkspaceID int64
		Settings    CodexRunSettings
		Revision    int64
		CreatedAt   time.Time
		UpdatedAt   time.Time
	}{settings.WorkspaceID, settings.Settings, settings.Revision, settings.CreatedAt, settings.UpdatedAt})
	if err != nil {
		panic("workspace run settings version encoding failed: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type WorkspaceRunSettingsConflict struct {
	Current SavedWorkspaceRunSettings
}

func (*WorkspaceRunSettingsConflict) Error() string {
	return "workspace run settings changed after they were loaded"
}

type WorkspaceRunSettingsDatabase interface {
	LoadWorkspaceRunSettings(context.Context, int64) (SavedWorkspaceRunSettings, error)
	SaveWorkspaceRunSettings(context.Context, SaveWorkspaceRunSettingsRequest) (SavedWorkspaceRunSettings, error)
	Close() error
}
