package storage

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"pellets/internal/domain"
)

const (
	MaxPlanningStateBytes    = 512 << 10
	MaxPlanningMessages      = 200
	MaxPlanningDrafts        = 200
	MaxPlanningTextBytes     = 64 << 10
	MaxPlanningComposerBytes = 32 << 10
)

// Planning is project-bound proposal state. It never represents queue ownership,
// an execution run, a process, or permission to create unselected work.
type PlanningMessage struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Text   string `json:"text"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}
type PlanningDraft struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Acceptance       string `json:"acceptance,omitempty"`
	Group            string `json:"group"`
	Reason           string `json:"reason,omitempty"`
	Selected         bool   `json:"selected"`
	CreatedNumber    int64  `json:"created_number,omitempty"`
	CreatedReference string `json:"created_reference,omitempty"`
}
type PlanningState struct {
	WorkspaceID     int64             `json:"workspace_id"`
	Model           string            `json:"model"`
	Effort          string            `json:"effort"`
	Composer        string            `json:"input"`
	Messages        []PlanningMessage `json:"messages"`
	Drafts          []PlanningDraft   `json:"drafts"`
	RefiningDraftID string            `json:"refining_draft_id,omitempty"`
}
type PlanningChat struct {
	ID          int64         `json:"id"`
	ProjectID   int64         `json:"project_id"`
	ProjectCode string        `json:"project_code"`
	Version     string        `json:"version"`
	State       PlanningState `json:"state"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}
type PlanningCreation struct {
	Chat    PlanningChat `json:"chat"`
	Pellets []Pellet     `json:"pellets"`
}
type PlanningReader interface {
	ReadPlanningChat(context.Context, Project, int64) (PlanningChat, error)
	// A nil result means this project has no chat. Reading never creates state.
	LatestPlanningChat(context.Context, Project) (*PlanningChat, error)
}
type PlanningWriter interface {
	CreatePlanningChat(context.Context, Project, string, PlanningState) (PlanningChat, error)
	SavePlanningChat(context.Context, Project, int64, string, PlanningState) (PlanningChat, error)
	CreatePlanningPellets(context.Context, Project, int64, string, []string) (PlanningCreation, error)
}

var planningID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

func ValidPlanningID(value string) bool { return planningID.MatchString(value) }
func planningText(value string, max int) bool {
	return len(value) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
func ValidatePlanningState(state PlanningState) error {
	if state.WorkspaceID < 0 || !planningText(state.Model, 512) || !planningText(state.Effort, 128) || strings.ContainsAny(state.Model+state.Effort, "\r\n") || !planningText(state.Composer, MaxPlanningComposerBytes) || len(state.Messages) > MaxPlanningMessages || len(state.Drafts) > MaxPlanningDrafts {
		return InvalidPlanningState("planning state exceeds its text or item limits")
	}
	messages := map[string]bool{}
	for _, m := range state.Messages {
		if !ValidPlanningID(m.ID) || messages[m.ID] || (m.Role != "user" && m.Role != "assistant") || !planningText(m.Text, MaxPlanningTextBytes) || strings.TrimSpace(m.Text) == "" || !planningText(m.Model, 512) || !planningText(m.Effort, 128) {
			return InvalidPlanningState("planning messages require unique IDs, user or assistant roles, and bounded text")
		}
		messages[m.ID] = true
	}
	drafts := map[string]bool{}
	for _, d := range state.Drafts {
		if !ValidPlanningID(d.ID) || drafts[d.ID] || !planningText(d.Title, 4096) || !planningText(d.Description, MaxPlanningTextBytes) || !planningText(d.Acceptance, MaxPlanningTextBytes) || !planningText(d.Group, 4096) || !planningText(d.Reason, 4096) || d.CreatedNumber < 0 || !planningText(d.CreatedReference, 64) {
			return InvalidPlanningState("planning drafts require unique IDs and bounded text")
		}
		drafts[d.ID] = true
	}
	if state.RefiningDraftID != "" && !drafts[state.RefiningDraftID] {
		return InvalidPlanningState("the refinement target must be a draft in this chat")
	}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > MaxPlanningStateBytes {
		return InvalidPlanningState("planning state exceeds its encoded storage limit; start a new chat or remove unneeded drafts")
	}
	return nil
}
func InvalidPlanningState(message string) error {
	return domain.NewError(domain.Usage, "invalid_planning_state", message, nil)
}
func PlanningChatNotFound(id int64) error {
	return domain.NewError(domain.NotFound, "planning_chat_not_found", "the planning chat is unavailable in the selected project", map[string]any{"chat_id": id})
}
func PlanningConflict(chat PlanningChat) error {
	return domain.NewError(domain.Conflict, "planning_chat_conflict", "this planning chat changed; reload its current state before applying your draft", map[string]any{"chat_id": chat.ID, "version": chat.Version})
}
func PlanningRequestConflict() error {
	return domain.NewError(domain.Conflict, "planning_request_conflict", "this planning request ID was already used with different initial content", nil)
}
