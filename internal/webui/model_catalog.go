package webui

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

func (h *handler) serveModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/models/refresh" {
		if r.Method != http.MethodPost {
			planningJSON(w, 405, map[string]string{"error": "Use POST to refresh models."})
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			planningJSON(w, 415, map[string]string{"error": "Refresh requires JSON."})
			return
		}
		var input struct {
			CSRF string `json:"_csrf"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
			planningJSON(w, 400, map[string]string{"error": "Invalid refresh request."})
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || !constantEqual(cookie.Value, h.config.CSRF) || !constantEqual(input.CSRF, h.config.CSRF) {
			planningJSON(w, 403, map[string]string{"error": "Invalid CSRF capability."})
			return
		}
	} else if r.Method != http.MethodGet {
		planningJSON(w, 405, map[string]string{"error": "Use GET for models."})
		return
	}
	service := h.application.ModelCatalog
	if service == nil {
		planningJSON(w, 503, map[string]string{"error": "Model catalog is unavailable."})
		return
	}
	if r.Method == http.MethodPost {
		service.Request(true)
		planningJSON(w, 202, map[string]bool{"refreshing": true})
		return
	}
	snapshot, err := service.Snapshot(r.Context())
	if err != nil {
		planningJSON(w, 503, map[string]string{"error": "Model catalog is unavailable."})
		return
	}
	planningJSON(w, 200, snapshot)
}
