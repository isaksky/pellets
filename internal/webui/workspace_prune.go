package webui

import (
	"net/http"
	"net/url"
	"strconv"

	"pellets/internal/app"
	"pellets/internal/storage"
)

func (h *handler) pruneWorkspaces(w http.ResponseWriter, r *http.Request, project storage.Project) {
	writer, ok := h.application.Writer.(storage.WorkspacePruner)
	if !ok {
		h.renderError(w, 503, requestError("workspace pruning is unavailable"), nil)
		return
	}
	result, err := app.PruneWorkspaces(r.Context(), h.application.Database, project, writer, false)
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	h.hub.broadcast()
	pageRequest := r.Clone(r.Context())
	pageRequest.Method = http.MethodGet
	pageRequest.URL = &url.URL{Path: "/projects/" + url.PathEscape(project.Code) + "/workspaces"}
	// Keep the selected execution workspace if pruning did not retire it.
	if id, e := strconv.ParseInt(r.PostForm.Get("execution"), 10, 64); e == nil {
		pruned := false
		for _, entry := range result.Pruned {
			if entry.ID == id {
				pruned = true
			}
		}
		for _, workspace := range project.Workspaces {
			if workspace.ID == id && !pruned {
				pageRequest.URL.RawQuery = url.Values{"execution": {strconv.FormatInt(id, 10)}}.Encode()
			}
		}
	}
	data, err := h.loadPage(pageRequest, project.Code, "workspaces", pathSegments(pageRequest.URL.Path))
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	data.PruneResult = &result
	if _, ok := w.(*datastarResponse); ok {
		h.render(w, 200, "app-content", data)
	} else {
		http.Redirect(w, r, data.CurrentURL, http.StatusSeeOther)
	}
}
