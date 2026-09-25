package webui

import (
	"context"
	"net/http"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type checkpointScopeChoice struct {
	Pellet   storage.Pellet
	Version  string
	Selected bool
}

type checkpointHistoryView struct {
	storage.CheckpointScopeHistory
	Outcome *checkpointOutcomeView
}

type checkpointManagementView struct {
	Open    bool
	Choices []checkpointScopeChoice
	History []checkpointHistoryView
}

func (h *handler) loadCheckpointManagement(ctx context.Context, project storage.Project, pellet storage.Pellet) (*checkpointManagementView, error) {
	view := &checkpointManagementView{}
	rows, err := h.application.Pellets(ctx, project, storage.WebPelletFilters{})
	if err != nil {
		return nil, err
	}
	selected := map[int64]bool{}
	if pellet.Checkpoint != nil {
		for _, target := range pellet.Checkpoint.Targets {
			selected[target.Number] = true
		}
	}
	for _, row := range rows {
		if row.Kind == domain.PelletOrdinary {
			view.Choices = append(view.Choices, checkpointScopeChoice{Pellet: row, Version: storage.PelletVersion(row), Selected: selected[row.Reference.Number]})
		}
	}
	history, err := h.application.CheckpointHistory(ctx, project, pellet.Reference)
	if err != nil {
		return nil, err
	}
	for _, entry := range history {
		generation := pellet
		generation.ImplementationRevision = entry.ImplementationRevision
		outcome, err := h.application.CheckpointOutcome(ctx, generation)
		if err != nil {
			return nil, err
		}
		view.History = append(view.History, checkpointHistoryView{CheckpointScopeHistory: entry, Outcome: makeCheckpointOutcomeView(outcome, project.Code, nil, storage.WebPelletSort{})})
	}
	return view, nil
}

// serveCheckpointMutation is called only after the common method, origin,
// bounded form-body and CSRF checks in serveMutation have succeeded.
func (h *handler) serveCheckpointMutation(response http.ResponseWriter, request *http.Request, project storage.Project, referenceText, action string) {
	draft := submittedDraft(request.PostForm)
	if values := request.PostForm["target"]; len(values) > 0 {
		draft["target"] = strings.Join(values, ", ")
	}
	reference, err := domain.ParsePelletReference(referenceText)
	if err != nil || !projectAcceptsCode(project, reference.ProjectCode) {
		h.renderError(response, http.StatusNotFound, requestError("checkpoint not found"), draft)
		return
	}
	reference.ProjectCode = project.Code
	if action != "scope" && action != "remove" && action != "restore" {
		http.NotFound(response, request)
		return
	}
	for name, values := range request.PostForm {
		if action == "scope" && name == "target" {
			continue
		}
		if (name != "_csrf" && name != "version") || len(values) != 1 {
			h.renderError(response, http.StatusUnprocessableEntity, requestError("the checkpoint request contains unexpected fields"), draft)
			return
		}
	}
	version := request.PostForm.Get("version")
	if !validVersion(version) {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("an exact checkpoint row version is required"), draft)
		return
	}
	var pellet storage.Pellet
	switch action {
	case "scope":
		var targets []storage.ReviewTargetVersion
		for _, selected := range request.PostForm["target"] {
			refText, targetVersion, found := strings.Cut(selected, ":")
			ref, parseErr := domain.ParsePelletReference(refText)
			if !found || parseErr != nil || !projectAcceptsCode(project, ref.ProjectCode) || !validVersion(targetVersion) {
				h.renderError(response, http.StatusUnprocessableEntity, requestError("every selected scope target requires its exact current row version"), draft)
				return
			}
			ref.ProjectCode = project.Code
			targets = append(targets, storage.ReviewTargetVersion{Reference: ref, Version: targetVersion})
		}
		pellet, err = h.application.UpdateCheckpointScope(request.Context(), project, reference, version, targets)
	case "remove":
		pellet, err = h.application.RemoveCheckpoint(request.Context(), project, reference, version)
	case "restore":
		pellet, err = h.application.RestoreCheckpoint(request.Context(), project, reference, version)
	}
	if err != nil {
		h.renderMutationError(response, err, draft)
		return
	}
	h.renderPelletResult(response, request, project.Code, pellet, http.StatusOK)
}
