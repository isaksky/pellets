package webui

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// This gallery deliberately renders fixtures, without reading or writing the
// database. It loads only presentation scripts, never the application's transport.
func (h *handler) serveDesignSystem(response http.ResponseWriter, request *http.Request) {
	if !h.config.Development {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var output bytes.Buffer
	if err := h.templates.ExecuteTemplate(&output, "design-system", designSystemFixtures()); err != nil {
		http.Error(response, "could not render design system", http.StatusInternalServerError)
		return
	}
	// Even without JavaScript, the real editor templates cannot submit fixtures.
	policy := response.Header().Get("Content-Security-Policy")
	response.Header().Set("Content-Security-Policy", strings.Replace(policy, "form-action 'self'", "form-action 'none'", 1))
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_, _ = response.Write(output.Bytes())
}

type designSystemData struct {
	UIRevision                           string
	Tokens                               []string
	Pellet, Memory, Checkpoint, Conflict pageData
}

func designSystemFixtures() designSystemData {
	created := time.Date(2026, time.January, 15, 10, 30, 0, 0, time.UTC)
	group := "Interface"
	pellet := storage.Pellet{
		Reference: domain.PelletReference{ProjectCode: "demo", Number: 42}, ProjectID: 1,
		Kind: domain.PelletOrdinary, Status: domain.PelletOpen,
		Title: "Make everyday work feel simple", Description: "A sample pellet for exploring the editor. Try changing its title, description, and group.",
		Group: &group, CreatedAt: created, UpdatedAt: created,
	}
	page := pageData{
		Project:  storage.Project{Code: "demo", Workspaces: []storage.Workspace{{ID: 1, RootPath: domain.LocalPath{Value: "main"}}}},
		CloseURL: "#dialogs", SelectedPellet: &pelletView{Pellet: pellet, Priority: "1024", Version: "preview"},
	}
	memory := pageData{CloseURL: "#dialogs", SelectedMemory: &memoryView{Memory: storage.Memory{
		ID: 7, ProjectID: 1, ProjectCode: "demo", CreatedBy: domain.MemoryCreatedByAgent,
		Text: "Use the shared theme tokens for all interface colors. Keep interactions accessible with a keyboard.", CreatedAt: created, UpdatedAt: created,
	}, Version: "preview"}}
	checkpoint := page
	checkpointPellet := pellet
	checkpointPellet.Reference.Number = 43
	checkpointPellet.Title = "Review interface changes"
	checkpointPellet.Kind = domain.PelletReviewCheckpoint
	checkpointPellet.ImplementationRevision = 1
	checkpointPellet.Checkpoint = &storage.ReviewCheckpoint{Version: 1, Targets: []storage.ReviewTarget{{Number: 42, Reference: "demo-42", Title: pellet.Title, Reason: "target_incomplete"}}}
	checkpoint.SelectedPellet = &pelletView{Pellet: checkpointPellet, Priority: "2048", Version: "preview", CheckpointManagement: &checkpointManagementView{Choices: []checkpointScopeChoice{{Pellet: pellet, Version: "preview", Selected: true}}}}
	checkpoint.SelectedPellet.URL = "#dialogs"
	checkpoint.SelectedPellet.ScopeRefs, checkpoint.SelectedPellet.ScopeTotal = "demo-42", 1
	checkpoint.SelectedPellet.Group = group
	checkpoint.SelectedPellet.ReviewStatus = "Waiting for 1 pellet"
	checkpoint.SelectedPellet.ReviewHelp = "Expand the scope for each target’s readiness and blocking reason."
	checkpoint.SelectedPellet.ScopeTargets = []checkpointTargetView{{Reference: "demo-42", Title: pellet.Title, Status: "Open", Reason: "Waiting for completion", URL: "#dialogs"}}
	checkpoint.SelectedPellet.CheckpointOutcome = makeCheckpointOutcomeView(storage.CheckpointOutcome{ImplementationRevision: 1, Status: "pending", Triage: "not_started"}, "demo", nil, storage.WebPelletSort{})
	conflict := page
	conflict.Conflict = &conflictView{Current: "Title: Make everyday work feel simple", Draft: map[string]string{"title": "An earlier draft of this title"}}
	return designSystemData{UIRevision: uiRevision, Tokens: []string{"bg", "pane", "lift", "line", "ink", "muted", "accent", "green", "amber", "red", "blue", "purple"}, Pellet: page, Memory: memory, Checkpoint: checkpoint, Conflict: conflict}
}
