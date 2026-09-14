package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"pellets/internal/storage"
)

func (h *handler) pageSettings() (string, error) {
	values := storage.Settings{}
	if reader, ok := h.application.Reader.(storage.SettingsReader); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		values, err = reader.ReadSettings(ctx)
		if err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err
}

func (h *handler) serveSettings(w http.ResponseWriter, r *http.Request) {
	reader, ok := h.application.Reader.(storage.SettingsReader)
	if !ok {
		planningJSON(w, 503, map[string]string{"error": "Settings storage is unavailable."})
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			planningJSON(w, 415, map[string]string{"error": "Settings require JSON."})
			return
		}
		var input struct {
			CSRF        string `json:"_csrf"`
			Key         string `json:"key"`
			Value       string `json:"value"`
			OnlyIfUnset bool   `json:"only_if_unset"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
			h.planningError(w, requestError("Invalid settings request."))
			return
		}
		cookie, err := r.Cookie(csrfCookieName)
		if err != nil || !constantEqual(cookie.Value, h.config.CSRF) || !constantEqual(input.CSRF, h.config.CSRF) {
			planningJSON(w, 403, map[string]string{"error": "Invalid CSRF capability."})
			return
		}
		writer, ok := h.application.Writer.(storage.SettingsWriter)
		if !ok {
			planningJSON(w, 503, map[string]string{"error": "Settings storage is unavailable."})
			return
		}
		if err := writer.SaveSetting(r.Context(), input.Key, input.Value, input.OnlyIfUnset); err != nil {
			h.planningError(w, err)
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		planningJSON(w, 405, map[string]string{"error": "Use GET or POST for settings."})
		return
	}
	values, err := reader.ReadSettings(r.Context())
	if err != nil {
		h.planningError(w, err)
		return
	}
	planningJSON(w, 200, map[string]any{"settings": values})
}
