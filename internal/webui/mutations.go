package webui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

const maxMutationBody = int64(2 << 20)

func (h *handler) serveMutation(response http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		h.renderError(response, http.StatusUnsupportedMediaType, requestError("mutations require application/x-www-form-urlencoded"), nil)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxMutationBody)
	if err := request.ParseForm(); err != nil {
		h.renderError(response, http.StatusBadRequest, requestError("could not parse the submitted form"), nil)
		return
	}
	cookie, err := request.Cookie(csrfCookieName)
	if err != nil || !constantEqual(cookie.Value, h.config.CSRF) || !constantEqual(request.PostForm.Get("_csrf"), h.config.CSRF) {
		h.renderError(response, http.StatusForbidden, requestError("invalid CSRF capability"), nil)
		return
	}

	segments := pathSegments(request.URL.Path)
	if len(segments) < 3 || segments[0] != "projects" {
		http.NotFound(response, request)
		return
	}
	projectSummary, err := h.application.Project(request.Context(), segments[1])
	if err != nil {
		h.renderError(response, statusForError(err), err, submittedDraft(request.PostForm))
		return
	}
	project := projectSummary.Project

	switch {
	case len(segments) == 3 && segments[2] == "pellets":
		h.createPellet(response, request, project)
	case len(segments) == 5 && segments[2] == "pellets":
		reference, parseErr := domain.ParsePelletReference(segments[3])
		if parseErr != nil || reference.ProjectCode != project.Code {
			h.renderError(response, http.StatusNotFound, requestError("pellet not found"), submittedDraft(request.PostForm))
			return
		}
		switch segments[4] {
		case "edit":
			h.editPellet(response, request, project, reference)
		case "move":
			h.movePellet(response, request, project, reference)
		case "transition":
			h.transitionPellet(response, request, project, reference)
		default:
			http.NotFound(response, request)
		}
	case len(segments) == 3 && segments[2] == "memories":
		h.createMemory(response, request, project)
	case len(segments) == 5 && segments[2] == "memories":
		memoryID, parseErr := domain.ParseMemoryID(segments[3])
		if parseErr != nil {
			h.renderError(response, http.StatusNotFound, requestError("memory not found"), submittedDraft(request.PostForm))
			return
		}
		switch segments[4] {
		case "edit":
			h.editMemory(response, request, project, memoryID)
		case "approve":
			h.approveMemory(response, request, project, memoryID)
		default:
			http.NotFound(response, request)
		}
	default:
		http.NotFound(response, request)
	}
}

func (h *handler) createPellet(response http.ResponseWriter, request *http.Request, project storage.Project) {
	if err := requireFields(request.PostForm, []string{"_csrf", "title", "description", "external_id", "group", "status"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	status := domain.PelletStatus(request.PostForm.Get("status"))
	if status != domain.PelletOpen && status != domain.PelletMaybeLater {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("new pellets must be open or maybe later"), submittedDraft(request.PostForm))
		return
	}
	input := storage.NewPellet{
		Title: request.PostForm.Get("title"), Description: request.PostForm.Get("description"),
		ExternalID: nullableInput(request.PostForm.Get("external_id")), Group: nullableInput(request.PostForm.Get("group")),
		Status: status,
	}
	pellet, err := h.application.CreatePellet(request.Context(), project, input)
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderPelletResult(response, request, project.Code, pellet, http.StatusCreated)
}

func (h *handler) editPellet(response http.ResponseWriter, request *http.Request, project storage.Project, reference domain.PelletReference) {
	if err := requireFields(request.PostForm, []string{"_csrf", "version", "title", "description", "external_id", "group"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	version := request.PostForm.Get("version")
	if !validVersion(version) {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("the edit version is invalid"), submittedDraft(request.PostForm))
		return
	}
	title, description := request.PostForm.Get("title"), request.PostForm.Get("description")
	changes := storage.PelletChanges{
		Title: &title, Description: &description,
		ExternalID: storage.NullableTextChange{Set: true, Value: nullableInput(request.PostForm.Get("external_id"))},
		Group:      storage.NullableTextChange{Set: true, Value: nullableInput(request.PostForm.Get("group"))},
	}
	pellet, err := h.application.UpdatePellet(request.Context(), project, reference, version, changes)
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderPelletResult(response, request, project.Code, pellet, http.StatusOK)
}

func (h *handler) movePellet(response http.ResponseWriter, request *http.Request, project storage.Project, reference domain.PelletReference) {
	if err := requireFields(request.PostForm, []string{"_csrf", "version", "target", "direction"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	version := request.PostForm.Get("version")
	target, err := domain.ParsePelletReference(request.PostForm.Get("target"))
	if !validVersion(version) || err != nil || target.ProjectCode != project.Code {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("the reorder request is invalid"), submittedDraft(request.PostForm))
		return
	}
	direction := request.PostForm.Get("direction")
	if direction != "before" && direction != "after" {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("the reorder direction must be before or after"), submittedDraft(request.PostForm))
		return
	}
	pellet, err := h.application.MovePellet(request.Context(), project, reference, version, storage.PelletPlacement{Target: target, Before: direction == "before"})
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderPelletResult(response, request, project.Code, pellet, http.StatusOK)
}

func (h *handler) transitionPellet(response http.ResponseWriter, request *http.Request, project storage.Project, reference domain.PelletReference) {
	if err := requireFields(request.PostForm, []string{"_csrf", "version", "operation", "recover_workspace_id", "confirm_recovery"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	version := request.PostForm.Get("version")
	operation := storage.PelletLifecycleOperation(request.PostForm.Get("operation"))
	if !validVersion(version) || !validLifecycleOperation(operation) {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("the lifecycle request is invalid"), submittedDraft(request.PostForm))
		return
	}
	transition := storage.PelletLifecycleRequest{Operation: operation}
	if raw := request.PostForm.Get("recover_workspace_id"); raw != "" {
		workspaceID, err := parsePositiveID(raw)
		if err != nil || request.PostForm.Get("confirm_recovery") != "yes" {
			h.renderError(response, http.StatusUnprocessableEntity, requestError("workspace recovery requires the exact owner and explicit confirmation"), submittedDraft(request.PostForm))
			return
		}
		transition.RecoveryWorkspaceID = &workspaceID
	}
	result, err := h.application.TransitionPellet(request.Context(), project, reference, version, transition)
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderPelletResult(response, request, project.Code, result.Pellet, http.StatusOK)
}

func (h *handler) createMemory(response http.ResponseWriter, request *http.Request, project storage.Project) {
	if err := requireFields(request.PostForm, []string{"_csrf", "text"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	text := request.PostForm.Get("text")
	if err := domain.ValidateMemoryText(text); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	// The local browser is a human-facing surface. Provenance is assigned by
	// the server and is never accepted from a client-controlled form field.
	memory, err := h.application.CreateMemory(request.Context(), project, storage.NewMemory{Text: text, CreatedBy: domain.MemoryCreatedByHuman})
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderMemoryResult(response, request, project.Code, memory, http.StatusCreated)
}

func (h *handler) editMemory(response http.ResponseWriter, request *http.Request, project storage.Project, memoryID int64) {
	if err := requireFields(request.PostForm, []string{"_csrf", "version", "text"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	version, text := request.PostForm.Get("version"), request.PostForm.Get("text")
	if !validVersion(version) {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("the edit version is invalid"), submittedDraft(request.PostForm))
		return
	}
	if err := domain.ValidateMemoryText(text); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	memory, err := h.application.UpdateMemory(request.Context(), project, memoryID, version, text)
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderMemoryResult(response, request, project.Code, memory, http.StatusOK)
}

func (h *handler) approveMemory(response http.ResponseWriter, request *http.Request, project storage.Project, memoryID int64) {
	if err := requireFields(request.PostForm, []string{"_csrf", "version"}); err != nil {
		h.renderError(response, http.StatusUnprocessableEntity, err, submittedDraft(request.PostForm))
		return
	}
	version := request.PostForm.Get("version")
	if !validVersion(version) {
		h.renderError(response, http.StatusUnprocessableEntity, requestError("the approval version is invalid"), submittedDraft(request.PostForm))
		return
	}
	memory, err := h.application.ApproveMemory(request.Context(), project, memoryID, version)
	if err != nil {
		h.renderMutationError(response, err, submittedDraft(request.PostForm))
		return
	}
	h.renderMemoryResult(response, request, project.Code, memory, http.StatusOK)
}

func (h *handler) renderPelletResult(response http.ResponseWriter, request *http.Request, code string, pellet storage.Pellet, status int) {
	path := "/projects/" + url.PathEscape(code) + "/tasks/" + url.PathEscape(pellet.Reference.String())
	refresh := request.Clone(request.Context())
	refresh.URL.Path, refresh.URL.RawQuery = path, ""
	data, err := h.loadPage(refresh, code, "tasks", pathSegments(path))
	if err != nil {
		h.renderError(response, statusForError(err), err, nil)
		return
	}
	response.Header().Set("HX-Trigger", "pellets:refresh")
	response.Header().Set("HX-Push-Url", path)
	h.render(response, status, "inspector", data)
}

func (h *handler) renderMemoryResult(response http.ResponseWriter, request *http.Request, code string, memory storage.Memory, status int) {
	path := "/projects/" + url.PathEscape(code) + "/memories/" + strconv.FormatInt(memory.ID, 10)
	refresh := request.Clone(request.Context())
	refresh.URL.Path, refresh.URL.RawQuery = path, ""
	data, err := h.loadPage(refresh, code, "memories", pathSegments(path))
	if err != nil {
		h.renderError(response, statusForError(err), err, nil)
		return
	}
	response.Header().Set("HX-Trigger", "pellets:refresh")
	response.Header().Set("HX-Push-Url", path)
	h.render(response, status, "inspector", data)
}

func (h *handler) renderMutationError(response http.ResponseWriter, err error, draft map[string]string) {
	status := statusForError(err)
	if status == http.StatusServiceUnavailable {
		response.Header().Set("Retry-After", "5")
	}
	var conflict *storage.OptimisticConflict
	if errors.As(err, &conflict) {
		data := pageData{CSRF: h.config.CSRF, Conflict: &conflictView{Draft: draft}, StatusCode: status}
		if conflict.Pellet != nil {
			views := makePelletViews([]storage.Pellet{*conflict.Pellet}, conflict.Pellet.Reference.ProjectCode, nil, conflict.Pellet.Reference.String(), nil, false)
			data.SelectedPellet = &views[0]
			data.Conflict.Kind = "pellet"
			data.Conflict.Current = fmt.Sprintf("%s · %s · updated %s", conflict.Pellet.Reference, conflict.Pellet.Title, formatTime(conflict.Pellet.UpdatedAt))
		} else if conflict.Memory != nil {
			views := makeMemoryViews([]storage.Memory{*conflict.Memory}, conflict.Memory.ProjectCode, conflict.Memory.ID)
			data.SelectedMemory = &views[0]
			data.Conflict.Kind = "memory"
			data.Conflict.Current = fmt.Sprintf("Memory %d · updated %s", conflict.Memory.ID, formatTime(conflict.Memory.UpdatedAt))
		}
		h.render(response, http.StatusConflict, "conflict", data)
		return
	}
	h.renderError(response, status, err, draft)
}

func requireFields(form url.Values, allowed []string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	for name, values := range form {
		if _, ok := allowedSet[name]; !ok {
			return fmt.Errorf("unexpected form field %q", name)
		}
		if len(values) != 1 {
			return fmt.Errorf("form field %q must occur exactly once", name)
		}
	}
	for _, name := range allowed {
		if _, ok := form[name]; !ok {
			return fmt.Errorf("missing form field %q", name)
		}
	}
	return nil
}

func submittedDraft(form url.Values) map[string]string {
	draft := make(map[string]string)
	for name, values := range form {
		if name == "_csrf" || name == "version" {
			continue
		}
		draft[name] = strings.Join(values, "\n")
	}
	return draft
}

func nullableInput(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func validVersion(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validLifecycleOperation(operation storage.PelletLifecycleOperation) bool {
	switch operation {
	case storage.PelletStart, storage.PelletRelease, storage.PelletClose, storage.PelletReopen, storage.PelletDefer:
		return true
	default:
		return false
	}
}

func parsePositiveID(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("invalid positive ID")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("invalid positive ID")
	}
	return parsed, nil
}
