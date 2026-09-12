package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"pellets/internal/app"
)

// The ordinary invalidation stream deliberately carries no transcript. Activity
// has an independent cursor and bounded fan-out so it never rebuilds draft forms.
func (h *handler) serveActivity(response http.ResponseWriter, request *http.Request) {
	parts := pathSegments(request.URL.Path)
	if len(parts) != 5 || parts[0] != "projects" || parts[2] != "runs" || parts[4] != "activity" {
		http.NotFound(response, request)
		return
	}
	runID, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || runID < 1 {
		http.Error(response, "invalid run ID", http.StatusUnprocessableEntity)
		return
	}
	afterText := request.URL.Query().Get("after")
	if request.Header.Get("Last-Event-ID") != "" {
		afterText = request.Header.Get("Last-Event-ID")
	}
	var after uint64
	if afterText != "" {
		after, err = strconv.ParseUint(afterText, 10, 64)
		if err != nil {
			http.Error(response, "invalid activity cursor", http.StatusUnprocessableEntity)
			return
		}
	}
	project, err := h.application.Project(request.Context(), parts[1])
	if err != nil {
		http.Error(response, "project unavailable", statusForError(err))
		return
	}
	supervisor := h.application.Executions
	if supervisor == nil || h.application.Database.Path == "" {
		http.Error(response, "execution activity unavailable", http.StatusServiceUnavailable)
		return
	}
	run, err := supervisor.ReadRun(request.Context(), h.application.Database, runID)
	if err != nil {
		http.Error(response, "attempt unavailable", statusForError(err))
		return
	}
	if run.ProjectID != project.Project.ID {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Query().Get("stream") != "1" {
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(response).Encode(supervisor.SnapshotActivity(h.application.Database, runID, after))
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "streaming unavailable", http.StatusInternalServerError)
		return
	}
	changes, unsubscribe, err := supervisor.SubscribeActivity(h.application.Database, runID)
	if err != nil {
		http.Error(response, "too many activity streams", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribe()
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	response.Header().Set("Connection", "keep-alive")
	controller := http.NewResponseController(response)
	send := func(snapshot app.ActivitySnapshot) bool {
		body, err := json.Marshal(snapshot)
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if _, err = fmt.Fprintf(response, "id: %d\nevent: pellets-activity\ndata: %s\n\n", snapshot.Cursor, body); err != nil {
			return false
		}
		flusher.Flush()
		after = snapshot.Cursor
		return true
	}
	// Subscribe before reading the snapshot to close the initial-read race.
	if !send(supervisor.SnapshotActivity(h.application.Database, runID, after)) {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-h.config.Stopping:
			return
		case <-changes:
			if !send(supervisor.SnapshotActivity(h.application.Database, runID, after)) {
				return
			}
		case <-keepalive.C:
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := io.WriteString(response, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
