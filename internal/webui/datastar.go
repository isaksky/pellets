package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// datastarResponse keeps ordinary HTTP responses intact and adapts rendered
// fragments to Datastar's SSE protocol. Datastar only processes HTTP 200 bodies;
// actionable application errors retain their status in the result signal.
type datastarResponse struct {
	http.ResponseWriter
	request *http.Request
}

func (response *datastarResponse) render(status int, name, path, elements string) {
	if status >= 400 && status != http.StatusConflict && status != http.StatusUnprocessableEntity {
		http.Error(response.ResponseWriter, http.StatusText(status), status)
		return
	}
	selector, mode := "#"+name, "replace"
	switch name {
	case "project-rail":
		selector = "#project-drawer"
	case "inspector", "conflict", "error":
		selector, mode = "#inspector-host", "inner"
	}
	if name == "error" && response.request.Method == http.MethodGet {
		// Filter errors belong to the requested region, not an unrelated inspector
		// that may contain unsaved edits.
		target := response.request.Header.Get("Pellets-Target")
		switch target {
		case "tasks-area", "task-list", "memory-list", "project-drawer", "workspace-strip", "project-record", "inspector-host":
			selector, mode = "#"+target, "inner"
		default:
			http.Error(response.ResponseWriter, "invalid fragment target", http.StatusBadRequest)
			return
		}
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(response, "event: datastar-patch-elements\ndata: selector %s\ndata: mode %s\n", selector, mode)
	// Each HTML line needs its own SSE data field, including blank lines. Normalize
	// carriage returns so submitted multiline text cannot become SSE control fields.
	elements = strings.ReplaceAll(strings.ReplaceAll(elements, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(elements, "\n") {
		_, _ = fmt.Fprintf(response, "data: elements %s\n", line)
	}
	_, _ = fmt.Fprint(response, "\n")
	result, _ := json.Marshal(map[string]any{"_webResult": map[string]any{
		"status":  status,
		"url":     path,
		"refresh": response.request.Method == http.MethodPost && status < 400,
	}})
	_, _ = fmt.Fprintf(response, "event: datastar-patch-signals\ndata: signals %s\n\n", result)
}
