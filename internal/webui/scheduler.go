package webui

import (
	"encoding/json"
	"net/http"
	"strconv"

	"pellets/internal/app"
	"pellets/internal/storage"
)

func (h *handler) startSchedule(w http.ResponseWriter, r *http.Request, project storage.Project) {
	fields := []string{"_csrf", "workspace_id", "mode"}
	for _, optional := range []string{"external_id", "group", "group_scope", "resume_pellet", "resume_from", "limit", "preflight_receipt"} {
		if _, exists := r.PostForm[optional]; exists {
			fields = append(fields, optional)
		}
	}
	if err := requireFields(r.PostForm, fields); err != nil {
		h.renderError(w, http.StatusUnprocessableEntity, err, nil)
		return
	}
	workspace, err := strconv.ParseInt(r.PostForm.Get("workspace_id"), 10, 64)
	if err != nil || workspace < 1 {
		h.renderError(w, http.StatusUnprocessableEntity, requestError("an explicit workspace_id is required"), nil)
		return
	}
	request := app.ScheduleRequest{Mode: r.PostForm.Get("mode")}
	if token, present := r.PostForm["preflight_receipt"]; present {
		if len(token[0]) != 64 {
			h.renderError(w, http.StatusUnprocessableEntity, requestError("invalid preflight receipt identity"), nil)
			return
		}
		for _, key := range []string{"external_id", "group", "group_scope"} {
			if _, present := r.PostForm[key]; present {
				h.renderError(w, http.StatusUnprocessableEntity, requestError("saved preflight filters must come from the exact receipt"), nil)
				return
			}
		}
		request.PreflightReceipt = token[0]
	}
	groupScope := r.PostForm.Get("group_scope")
	if groupScope == "ungrouped" {
		h.renderError(w, http.StatusUnprocessableEntity, requestError("scheduling an exact ungrouped filter is not supported; remove the group filter before running"), nil)
		return
	}
	if scope, exists := r.PostForm["group_scope"]; exists && scope[0] != "any" && scope[0] != "value" && scope[0] != "ungrouped" {
		h.renderError(w, http.StatusUnprocessableEntity, requestError("invalid group filter scope"), nil)
		return
	}
	_, hasGroup := r.PostForm["group"]
	if (groupScope == "value" && (!hasGroup || r.PostForm.Get("group") == "")) || (groupScope == "any" && hasGroup) {
		h.renderError(w, http.StatusUnprocessableEntity, requestError("group filter scope does not match the exact group value"), nil)
		return
	}
	for key, target := range map[string]**string{"external_id": &request.ExternalID, "group": &request.Group} {
		if values, ok := r.PostForm[key]; ok {
			value := values[0]
			*target = &value
		}
	}
	for key, target := range map[string]**int64{"resume_pellet": &request.ResumePellet, "resume_from": &request.ResumeFrom} {
		if value, ok := r.PostForm[key]; ok {
			number, err := strconv.ParseInt(value[0], 10, 64)
			if err != nil || number < 1 {
				h.renderError(w, http.StatusUnprocessableEntity, requestError("invalid exact Resume identity"), nil)
				return
			}
			*target = &number
		}
	}
	if values, ok := r.PostForm["limit"]; ok {
		request.Limit, err = strconv.Atoi(values[0])
		if err != nil || request.Limit < 1 {
			h.renderError(w, http.StatusUnprocessableEntity, requestError("invalid schedule limit"), nil)
			return
		}
	}
	receipt, err := h.application.StartSchedule(r.Context(), project, workspace, request)
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	writeSchedule(w, http.StatusAccepted, receipt.Status())
}

func (h *handler) scheduleForProject(project storage.Project, value string) (*app.ScheduleHandle, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id < 1 || h.application.Scheduler == nil {
		return nil, requestError("schedule unavailable")
	}
	receipt, err := h.application.Scheduler.Get(id)
	if err != nil {
		return nil, err
	}
	if receipt.Status().ProjectID != project.ID {
		return nil, requestError("schedule belongs to another project")
	}
	return receipt, nil
}

func (h *handler) readSchedule(w http.ResponseWriter, r *http.Request) {
	parts := pathSegments(r.URL.Path)
	if parts[0] != "projects" {
		http.NotFound(w, r)
		return
	}
	project, err := h.application.Project(r.Context(), parts[1])
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	receipt, err := h.scheduleForProject(project.Project, parts[3])
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	writeSchedule(w, http.StatusOK, receipt.Status())
}

func (h *handler) stopSchedule(w http.ResponseWriter, r *http.Request, project storage.Project, id, action string) {
	if err := requireFields(r.PostForm, []string{"_csrf"}); err != nil {
		h.renderError(w, http.StatusUnprocessableEntity, err, nil)
		return
	}
	receipt, err := h.scheduleForProject(project, id)
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	switch action {
	case "stop-after":
		receipt.StopAfter()
	case "stop-now":
		receipt.StopNow()
	default:
		http.NotFound(w, r)
		return
	}
	writeSchedule(w, http.StatusAccepted, receipt.Status())
}

func writeSchedule(w http.ResponseWriter, status int, value app.ScheduleStatus) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
