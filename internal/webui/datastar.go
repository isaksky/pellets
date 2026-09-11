package webui

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/starfederation/datastar-go/datastar"
)

// datastarResponse keeps ordinary HTTP responses intact and adapts rendered
// fragments to Datastar's SSE protocol. Datastar only processes HTTP 200 bodies;
// actionable application errors retain their status in the result signal.
type datastarResponse struct {
	http.ResponseWriter
	request *http.Request
	stream  *datastar.ServerSentEventGenerator
	err     error
}

func (response *datastarResponse) render(status int, name, path, elements string) {
	if status >= 400 && status != http.StatusConflict && status != http.StatusUnprocessableEntity {
		http.Error(response.ResponseWriter, http.StatusText(status), status)
		return
	}
	selector, mode := "#"+name, "outer"
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
		case "tasks-area", "task-list", "memory-list", "project-drawer", "project-record", "run-dashboard", "inspector-host":
			selector, mode = "#"+target, "inner"
		default:
			http.Error(response.ResponseWriter, "invalid fragment target", http.StatusBadRequest)
			return
		}
	}
	response.start()
	response.patch(selector, mode, elements)
	response.result(status, path)
}

// The SDK flushes headers immediately. Keep our no-store policy by applying it
// after its defaults, and pass the underlying writer so flushing is supported.
func (response *datastarResponse) start() {
	response.Header().Set("X-Accel-Buffering", "no")
	response.stream = datastar.NewSSE(response.ResponseWriter, response.request, func(*datastar.ServerSentEventGenerator) {
		response.Header().Set("Cache-Control", "no-store")
	})
}

func (response *datastarResponse) patch(selector, mode, elements string) {
	if response.err != nil {
		return
	}
	// Normalize submitted carriage returns before the SDK splits SSE data lines.
	elements = strings.ReplaceAll(strings.ReplaceAll(elements, "\r\n", "\n"), "\r", "\n")
	response.recordError(response.stream.PatchElements(elements,
		datastar.WithSelector(selector), datastar.WithMode(datastar.ElementPatchMode(mode))))
}

func (response *datastarResponse) result(status int, path string) {
	if response.err != nil {
		return
	}
	result, err := json.Marshal(map[string]any{"_webResult": map[string]any{
		"status": status, "url": path,
	}})
	if err != nil {
		response.recordError(err)
		return
	}
	response.recordError(response.stream.PatchSignals(result))
}

// Stop a partially delivered bundle on its first write/flush failure. In
// particular, never send a completion signal after a failed element patch.
func (response *datastarResponse) recordError(err error) {
	if err != nil {
		response.err = err
		if response.request.Context().Err() == nil {
			log.Printf("web UI SSE response failed: %v", err)
		}
	}
}
