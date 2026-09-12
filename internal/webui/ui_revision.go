package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

const uiRevisionHeader = "Pellets-UI-Revision"

// Changing this tag also invalidates clients when the fragment protocol changes
// without changing embedded presentation files.
const uiRevisionProtocol = "pellets-web-ui-v1"

var uiRevision = mustUIRevision()

func mustUIRevision() string {
	revision, err := hashUIRevision(embeddedFiles)
	if err != nil {
		panic(fmt.Sprintf("hash embedded web UI: %v", err))
	}
	return revision
}

func hashUIRevision(files fs.FS) (string, error) {
	hash := sha256.New()
	fmt.Fprintln(hash, uiRevisionProtocol)
	for _, root := range []string{"assets", "templates"} {
		if err := fs.WalkDir(files, root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			content, err := fs.ReadFile(files, path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%d:%s:%d:", len(path), path, len(content))
			_, _ = hash.Write(content)
			return nil
		}); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeUIRevisionChanged(response http.ResponseWriter) {
	response.Header().Set(uiRevisionHeader, uiRevision)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(http.StatusPreconditionFailed)
	_ = json.NewEncoder(response).Encode(map[string]string{"code": "ui_revision_changed", "revision": uiRevision})
}

// Reject incompatible clients before loading records or admitting mutations.
// Ordinary non-Datastar requests without a revision retain their API behavior.
func acceptUIRevision(response http.ResponseWriter, request *http.Request) bool {
	revision := request.Header.Get(uiRevisionHeader)
	if (request.Header.Get("Datastar-Request") == "true" && revision != uiRevision) || (revision != "" && revision != uiRevision) {
		writeUIRevisionChanged(response)
		return false
	}
	query := request.URL.Query().Get("ui_revision")
	stream := request.URL.Path == "/events" || (strings.HasSuffix(request.URL.Path, "/activity") && request.URL.Query().Get("stream") == "1")
	if query != "" && query != uiRevision && !stream {
		writeUIRevisionChanged(response)
		return false
	}
	return true
}

func serveUIVersion(response http.ResponseWriter) {
	response.Header().Set(uiRevisionHeader, uiRevision)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(response).Encode(map[string]string{"revision": uiRevision})
}

func writeUIRevisionEvent(response http.ResponseWriter) bool {
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "event streaming unavailable", http.StatusInternalServerError)
		return false
	}
	response.Header().Set(uiRevisionHeader, uiRevision)
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("X-Accel-Buffering", "no")
	response.Header().Set("Connection", "keep-alive")
	body, _ := json.Marshal(map[string]string{"revision": uiRevision})
	controller := http.NewResponseController(response)
	_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(response, "event: pellets-ui-revision\ndata: %s\n\n", body); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func hasStaleUIQuery(request *http.Request) bool {
	revision := request.URL.Query().Get("ui_revision")
	return revision != "" && revision != uiRevision
}
