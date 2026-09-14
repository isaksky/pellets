package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

func isPlanningPath(path string) bool {
	p := pathSegments(path)
	return len(p) >= 3 && p[0] == "projects" && p[2] == "planning"
}

type planningRequest struct {
	CSRF      string                `json:"_csrf"`
	Action    string                `json:"action"`
	ChatID    int64                 `json:"chat_id"`
	Version   string                `json:"version"`
	RequestID string                `json:"request_id"`
	State     storage.PlanningState `json:"state"`
	DraftIDs  []string              `json:"draft_ids"`
}

func planningJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *handler) planningError(w http.ResponseWriter, err error) {
	public := domain.PublicError(err)
	planningJSON(w, statusForError(err), map[string]any{"error": public.Message, "code": public.Code})
}

func (h *handler) planningSnapshot(ctx context.Context, project storage.Project, chat *storage.PlanningChat) (map[string]any, error) {
	groups, err := h.application.Groups(ctx, project)
	if err != nil {
		return nil, err
	}
	values := []string{}
	for _, g := range groups {
		if g != nil {
			values = append(values, *g)
		}
	}
	routing, err := h.application.Routing(ctx, project)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, workspace := range project.Workspaces {
		a := routing.Assignment(workspace.ID)
		name := filepath.Base(workspace.RootPath.Value)
		if absolute, resolveErr := discovery.ResolveLocalPath(h.application.Database.Root, workspace.RootPath); resolveErr == nil {
			name = filepath.Base(absolute)
		}
		rows = append(rows, map[string]any{"id": workspace.ID, "name": name, "mode": a.Mode, "groups": a.Groups, "include_ungrouped": a.IncludeUngrouped})
	}
	path := project.GitCommonDir.Value
	if len(project.Workspaces) > 0 {
		path = project.Workspaces[0].RootPath.Value
		if absolute, resolveErr := discovery.ResolveLocalPath(h.application.Database.Root, project.Workspaces[0].RootPath); resolveErr == nil {
			path = absolute
		}
	}
	return map[string]any{"chat": chat, "project": map[string]any{"id": project.ID, "code": project.Code, "path": path}, "groups": values, "routing": rows, "assignments_enabled": routing.Enabled}, nil
}

func (h *handler) servePlanning(w http.ResponseWriter, r *http.Request) {
	parts := pathSegments(r.URL.Path)
	if len(parts) > 4 || (len(parts) == 4 && parts[3] != "models") {
		http.NotFound(w, r)
		return
	}
	summary, err := h.application.Project(r.Context(), parts[1])
	if err != nil {
		h.planningError(w, err)
		return
	}
	project := summary.Project
	if len(parts) == 4 {
		if r.Method != http.MethodGet {
			planningJSON(w, 405, map[string]string{"error": "Use GET for model choices."})
			return
		}
		if !h.takePlanningJob() {
			planningJSON(w, 409, map[string]string{"error": "The planner is busy. Try again shortly.", "code": "planning_busy"})
			return
		}
		defer h.releasePlanningJob()
		ctx, cancel := h.planningContext(r.Context())
		defer cancel()
		models, err := h.application.PlanningModels(ctx, project)
		if err != nil {
			h.planningError(w, err)
			return
		}
		planningJSON(w, 200, map[string]any{"models": models})
		return
	}
	reader, ok := h.application.Reader.(storage.PlanningReader)
	if !ok {
		planningJSON(w, 503, map[string]string{"error": "Planning storage is unavailable."})
		return
	}
	var chat *storage.PlanningChat
	if r.Method == http.MethodGet {
		idText := r.URL.Query().Get("chat")
		if idText != "" {
			id, parseErr := strconv.ParseInt(idText, 10, 64)
			if parseErr != nil || id < 1 {
				h.planningError(w, requestError("Choose an existing chat."))
				return
			}
			value, readErr := reader.ReadPlanningChat(r.Context(), project, id)
			err = readErr
			chat = &value
		} else {
			chat, err = reader.LatestPlanningChat(r.Context(), project)
		}
		if err != nil {
			h.planningError(w, err)
			return
		}
	} else if r.Method == http.MethodPost {
		media, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if parseErr != nil || media != "application/json" {
			planningJSON(w, 415, map[string]string{"error": "Planning requests require JSON."})
			return
		}
		var input planningRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err != nil {
			h.planningError(w, requestError("Invalid or oversized planning request."))
			return
		}
		if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			h.planningError(w, requestError("Send one planning request."))
			return
		}
		cookie, cookieErr := r.Cookie(csrfCookieName)
		if cookieErr != nil || !constantEqual(cookie.Value, h.config.CSRF) || !constantEqual(input.CSRF, h.config.CSRF) {
			planningJSON(w, 403, map[string]string{"error": "Invalid CSRF capability."})
			return
		}
		writer, ok := h.application.Writer.(storage.PlanningWriter)
		if !ok {
			planningJSON(w, 503, map[string]string{"error": "Planning storage is unavailable."})
			return
		}
		// All mutations of one chat are serialized without holding a database
		// transaction across model work. Other chats and execution remain independent.
		h.planningMu.Lock()
		if h.planningBusy == nil {
			h.planningBusy = map[int64]bool{}
		}
		if h.planningBusy[input.ChatID] || len(h.planningBusy) >= 4 {
			h.planningMu.Unlock()
			planningJSON(w, 409, map[string]string{"error": "A planning request is still running. Your edits have been kept.", "code": "planning_busy"})
			return
		}
		h.planningBusy[input.ChatID] = true
		h.planningMu.Unlock()
		defer func() { h.planningMu.Lock(); delete(h.planningBusy, input.ChatID); h.planningMu.Unlock() }()
		var value storage.PlanningChat
		switch input.Action {
		case "new":
			if len(input.State.Messages) != 0 {
				err = requestError("New chats begin without assistant or user history.")
			} else {
				value, err = writer.CreatePlanningChat(r.Context(), project, input.RequestID, input.State)
			}
		case "save":
			var current storage.PlanningChat
			current, err = reader.ReadPlanningChat(r.Context(), project, input.ChatID)
			if err == nil && !slices.Equal(current.State.Messages, input.State.Messages) {
				if current.Version != input.Version {
					err = storage.PlanningConflict(current)
				} else {
					err = requestError("Saved conversation messages cannot be changed. Send a message or start a new chat.")
				}
			}
			if err == nil {
				value, err = writer.SavePlanningChat(r.Context(), project, input.ChatID, input.Version, input.State)
			}
		case "create":
			var result storage.PlanningCreation
			result, err = writer.CreatePlanningPellets(r.Context(), project, input.ChatID, input.Version, input.DraftIDs)
			value = result.Chat
		case "send":
			if !h.takePlanningJob() {
				planningJSON(w, 409, map[string]string{"error": "The planner is busy. Try again shortly.", "code": "planning_busy"})
				return
			}
			defer h.releasePlanningJob()
			ctx, cancel := h.planningContext(r.Context())
			defer cancel()
			value, err = h.application.SendPlanningMessage(ctx, project, input.ChatID, input.Version, input.RequestID, input.State)
		default:
			err = requestError("Choose a planning action.")
		}
		if err != nil {
			h.planningError(w, err)
			return
		}
		chat = &value
	} else {
		w.Header().Set("Allow", "GET, POST")
		planningJSON(w, 405, map[string]string{"error": "Method not allowed."})
		return
	}
	result, err := h.planningSnapshot(r.Context(), project, chat)
	if err != nil {
		h.planningError(w, err)
		return
	}
	planningJSON(w, 200, result)
}

func (h *handler) takePlanningJob() bool {
	h.planningMu.Lock()
	defer h.planningMu.Unlock()
	if h.planningJobs >= 4 {
		return false
	}
	h.planningJobs++
	return true
}
func (h *handler) releasePlanningJob() { h.planningMu.Lock(); h.planningJobs--; h.planningMu.Unlock() }

func (h *handler) planningContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	go func() {
		select {
		case <-h.config.Stopping:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
