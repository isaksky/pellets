package webui

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

//go:embed templates/*.html assets/*
var embeddedFiles embed.FS

const csrfCookieName = "pl_web_csrf"

type handlerConfig struct {
	Host           string
	Origin         string
	CSRF           string
	InitialProject string
}

type handler struct {
	application *app.WebApplication
	hub         *eventHub
	config      handlerConfig
	templates   *template.Template
}

func newHandler(application *app.WebApplication, hub *eventHub, config handlerConfig) (http.Handler, error) {
	functions := template.FuncMap{
		"statusLabel": statusLabel,
		"formatTime":  formatTime,
		"text":        nullableText,
		"path":        localPath,
		"eqStatus":    func(left domain.PelletStatus, right string) bool { return string(left) == right },
		"sameID":      func(left, right int64) bool { return left == right },
		"lifecycle": func(page pageData, operation string) lifecycleFormView {
			return lifecycleFormView{Page: page, Operation: operation, Label: statusLabel(operation)}
		},
	}
	templates, err := template.New("pellets").Funcs(functions).ParseFS(embeddedFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	instance := &handler{application: application, hub: hub, config: config, templates: templates}
	return instance.securityHeaders(instance), nil
}

func (h *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/assets/"):
		h.serveAsset(response, request)
	case request.Method == http.MethodGet && request.URL.Path == "/events":
		h.serveEvents(response, request)
	case request.Method == http.MethodGet:
		h.servePage(response, request)
	case request.Method == http.MethodPost:
		h.serveMutation(response, request)
	default:
		response.Header().Set("Allow", "GET, POST")
		h.renderError(response, http.StatusMethodNotAllowed, requestError("method not allowed"), nil)
	}
}

func (h *handler) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'")
		response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		if request.Host != h.config.Host {
			http.Error(response, "invalid Host", http.StatusMisdirectedRequest)
			return
		}
		if request.Method == http.MethodPost && request.Header.Get("Origin") != h.config.Origin {
			http.Error(response, "invalid Origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (h *handler) serveAsset(response http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(request.URL.Path, "/assets/")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		http.NotFound(response, request)
		return
	}
	content, err := embeddedFiles.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		response.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		response.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		response.Header().Set("Content-Type", "application/octet-stream")
	}
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = response.Write(content)
}

func (h *handler) serveEvents(response http.ResponseWriter, request *http.Request) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		http.Error(response, "event streaming unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")
	events, unsubscribe := h.hub.subscribe()
	defer unsubscribe()
	controller := http.NewResponseController(response)
	_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.WriteString(response, "retry: 1500\n: connected\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-events:
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := io.WriteString(response, "event: pellets-invalidate\ndata: refresh\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			_ = controller.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := io.WriteString(response, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type pageData struct {
	CSRF             string
	Projects         []projectView
	Project          storage.Project
	ProjectSummary   storage.WebProjectSummary
	HasProject       bool
	MultiProject     bool
	CurrentProject   bool
	CurrentWorkspace *storage.Workspace
	Area             string
	TasksURL         string
	MemoriesURL      string
	CurrentURL       string
	CloseURL         string
	Pellets          []pelletView
	Memories         []memoryView
	Groups           []groupView
	Filters          filterView
	SelectedPellet   *pelletView
	SelectedMemory   *memoryView
	MoveTargets      []pelletView
	Flash            string
	Conflict         *conflictView
	Error            string
	StatusCode       int
}

type projectView struct {
	Code        string
	URL         string
	Active      bool
	Open        int64
	InProgress  int64
	Closed      int64
	MaybeLater  int64
	MemoryCount int64
	Workspaces  []workspaceView
}

type workspaceView struct {
	ID      int64
	Root    string
	GitDir  string
	Current bool
	Pellet  string
}

type pelletView struct {
	Pellet       storage.Pellet
	Version      string
	URL          string
	Selected     bool
	OwnerCurrent bool
	CanLifecycle bool
	Group        string
	ExternalID   string
	Priority     string
	Owner        string
}

type memoryView struct {
	Memory   storage.Memory
	Version  string
	URL      string
	Selected bool
}

type groupView struct {
	Value    string
	Label    string
	Selected bool
}

type filterView struct {
	Status     string
	Group      string
	ExternalID string
	Query      string
}

type conflictView struct {
	Kind    string
	Current string
	Draft   map[string]string
}

type lifecycleFormView struct {
	Page      pageData
	Operation string
	Label     string
}

func (h *handler) servePage(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/" {
		h.serveRoot(response, request)
		return
	}
	segments := pathSegments(request.URL.Path)
	if len(segments) < 3 || segments[0] != "projects" {
		http.NotFound(response, request)
		return
	}
	code, area := segments[1], segments[2]
	if area != "tasks" && area != "memories" || len(segments) > 4 {
		http.NotFound(response, request)
		return
	}
	selected, err := h.application.Project(request.Context(), code)
	if err != nil {
		h.renderError(response, statusForError(err), err, nil)
		return
	}
	if canonicalPath, changed := canonicalProjectDeepLink(segments, selected.Project); changed {
		if request.URL.RawQuery != "" {
			canonicalPath += "?" + request.URL.RawQuery
		}
		http.Redirect(response, request, canonicalPath, http.StatusTemporaryRedirect)
		return
	}
	data, err := h.loadPage(request, code, area, segments)
	if err != nil {
		h.renderError(response, statusForError(err), err, nil)
		return
	}
	h.setCSRFCookie(response)
	templateName := "page"
	if request.Header.Get("HX-Request") == "true" {
		switch request.Header.Get("HX-Target") {
		case "task-list":
			templateName = "task-list"
		case "memory-list":
			templateName = "memory-list"
		case "project-drawer":
			templateName = "project-rail"
		case "workspace-strip":
			templateName = "workspace-strip"
		case "project-record":
			templateName = "project-record"
		case "inspector-host":
			templateName = "inspector"
		}
	}
	h.render(response, http.StatusOK, templateName, data)
}

func (h *handler) serveRoot(response http.ResponseWriter, request *http.Request) {
	projects, err := h.application.Projects(request.Context())
	if err != nil {
		h.renderError(response, statusForError(err), err, nil)
		return
	}
	if len(projects) == 0 {
		h.setCSRFCookie(response)
		h.render(response, http.StatusOK, "page", pageData{CSRF: h.config.CSRF})
		return
	}
	code := h.config.InitialProject
	if code == "" && h.application.Current != nil {
		code = h.application.Current.Project.Code
	}
	if code != "" {
		if selected, err := h.application.Project(request.Context(), code); err == nil {
			code = selected.Project.Code
		}
	}
	if code == "" || !slices.ContainsFunc(projects, func(project storage.WebProjectSummary) bool { return project.Project.Code == code }) {
		code = projects[0].Project.Code
	}
	http.Redirect(response, request, "/projects/"+url.PathEscape(code)+"/tasks", http.StatusSeeOther)
}

func (h *handler) loadPage(request *http.Request, code, area string, segments []string) (pageData, error) {
	projects, err := h.application.Projects(request.Context())
	if err != nil {
		return pageData{}, err
	}
	var selected storage.WebProjectSummary
	found := false
	for _, project := range projects {
		if project.Project.Code == code {
			selected, found = project, true
			break
		}
	}
	if !found {
		return pageData{}, domain.NewError(domain.NotFound, "project_not_registered", "the project is not registered in this Pellets database", map[string]any{"code": code})
	}
	data := pageData{
		CSRF: h.config.CSRF, Project: selected.Project, ProjectSummary: selected, HasProject: true,
		MultiProject: len(projects) > 1, Area: area,
		TasksURL:    "/projects/" + url.PathEscape(code) + "/tasks",
		MemoriesURL: "/projects/" + url.PathEscape(code) + "/memories",
		CurrentURL:  request.URL.RequestURI(),
	}
	if h.application.Current != nil && h.application.Current.Project.ID == selected.Project.ID {
		data.CurrentProject = true
		workspace := h.application.Current.Workspace
		data.CurrentWorkspace = &workspace
	}
	data.Projects, err = h.projectViews(request, projects, code, area)
	if err != nil {
		return pageData{}, err
	}
	if area == "tasks" {
		var selectedReference domain.PelletReference
		selectedReferenceText := ""
		if len(segments) == 4 {
			selectedReference, err = domain.ParsePelletReference(segments[3])
			if err != nil || !projectAcceptsCode(selected.Project, selectedReference.ProjectCode) {
				return pageData{}, domain.NewError(domain.NotFound, "pellet_not_found", "the pellet does not exist in the selected project", nil)
			}
			selectedReference.ProjectCode = selected.Project.Code
			selectedReferenceText = selectedReference.String()
		}
		filters, view, err := parseFilters(request.URL.Query())
		if err != nil {
			return pageData{}, err
		}
		data.Filters = view
		pellets, err := h.application.Pellets(request.Context(), selected.Project, filters)
		if err != nil {
			return pageData{}, err
		}
		data.Pellets = makePelletViews(pellets, selected.Project.Code, request.URL.Query(), selectedReferenceText, data.CurrentWorkspace, data.CurrentProject)
		groups, err := h.application.Groups(request.Context(), selected.Project)
		if err != nil {
			return pageData{}, err
		}
		data.Groups = makeGroupViews(groups, view.Group)
		if selectedReferenceText != "" {
			pellet, err := h.application.Pellet(request.Context(), selected.Project, selectedReference)
			if err != nil {
				return pageData{}, err
			}
			views := makePelletViews([]storage.Pellet{pellet}, code, request.URL.Query(), selectedReferenceText, data.CurrentWorkspace, data.CurrentProject)
			data.SelectedPellet = &views[0]
			data.CloseURL = taskURL(code, request.URL.Query(), "")
			active, err := h.application.Pellets(request.Context(), selected.Project, storage.WebPelletFilters{})
			if err != nil {
				return pageData{}, err
			}
			for _, candidate := range active {
				if candidate.Reference != selectedReference && (candidate.Status == domain.PelletOpen || candidate.Status == domain.PelletInProgress) {
					data.MoveTargets = append(data.MoveTargets, makePelletViews([]storage.Pellet{candidate}, code, nil, "", data.CurrentWorkspace, data.CurrentProject)[0])
				}
			}
		}
	} else {
		selectedMemoryID := int64(0)
		if len(segments) == 4 {
			selectedMemoryID, err = domain.ParseMemoryID(segments[3])
			if err != nil {
				return pageData{}, err
			}
		}
		memories, err := h.application.Memories(request.Context(), selected.Project)
		if err != nil {
			return pageData{}, err
		}
		data.Memories = makeMemoryViews(memories, code, selectedMemoryID)
		if selectedMemoryID != 0 {
			memory, err := h.application.Memory(request.Context(), selected.Project, selectedMemoryID)
			if err != nil {
				return pageData{}, err
			}
			views := makeMemoryViews([]storage.Memory{memory}, code, selectedMemoryID)
			data.SelectedMemory = &views[0]
			data.CloseURL = data.MemoriesURL
		}
	}
	return data, nil
}

func projectAcceptsCode(project storage.Project, code string) bool {
	if project.Code == code {
		return true
	}
	for _, redirect := range project.Redirects {
		if redirect.Code == code {
			return true
		}
	}
	return false
}

func canonicalProjectDeepLink(segments []string, project storage.Project) (string, bool) {
	canonical := append([]string(nil), segments...)
	changed := canonical[1] != project.Code
	canonical[1] = project.Code
	if len(canonical) == 4 && canonical[2] == "tasks" {
		if reference, err := domain.ParsePelletReference(canonical[3]); err == nil && projectAcceptsCode(project, reference.ProjectCode) {
			if reference.ProjectCode != project.Code {
				changed = true
			}
			reference.ProjectCode = project.Code
			canonical[3] = reference.String()
		}
	}
	if !changed {
		return "", false
	}
	for index := range canonical {
		canonical[index] = url.PathEscape(canonical[index])
	}
	return "/" + strings.Join(canonical, "/"), true
}

func (h *handler) projectViews(request *http.Request, projects []storage.WebProjectSummary, activeCode, area string) ([]projectView, error) {
	views := make([]projectView, 0, len(projects))
	status := domain.PelletInProgress
	for _, summary := range projects {
		view := projectView{
			Code: summary.Project.Code, Active: summary.Project.Code == activeCode,
			Open: summary.Open, InProgress: summary.InProgress, Closed: summary.Closed,
			MaybeLater: summary.MaybeLater, MemoryCount: summary.MemoryCount,
			URL: "/projects/" + url.PathEscape(summary.Project.Code) + "/" + area,
		}
		inProgress, err := h.application.Pellets(request.Context(), summary.Project, storage.WebPelletFilters{Status: &status})
		if err != nil {
			return nil, err
		}
		owners := make(map[int64]string, len(inProgress))
		for _, pellet := range inProgress {
			if pellet.Workspace != nil {
				owners[pellet.Workspace.ID] = pellet.Reference.String()
			}
		}
		for _, workspace := range summary.Project.Workspaces {
			current := h.application.Current != nil && h.application.Current.Workspace.ID == workspace.ID
			view.Workspaces = append(view.Workspaces, workspaceView{
				ID: workspace.ID, Root: localPath(workspace.RootPath), GitDir: localPath(workspace.GitDir),
				Current: current, Pellet: owners[workspace.ID],
			})
		}
		views = append(views, view)
	}
	return views, nil
}

func makePelletViews(pellets []storage.Pellet, code string, query url.Values, selected string, current *storage.Workspace, currentProject bool) []pelletView {
	views := make([]pelletView, 0, len(pellets))
	for _, pellet := range pellets {
		view := pelletView{
			Pellet: pellet, Version: storage.PelletVersion(pellet),
			URL: taskURL(code, query, pellet.Reference.String()), Selected: pellet.Reference.String() == selected,
			Group: textOrDash(pellet.Group), ExternalID: textOrDash(pellet.ExternalID), Priority: "—",
			CanLifecycle: currentProject,
		}
		if pellet.Priority != nil {
			view.Priority = strconv.FormatInt(*pellet.Priority, 10)
		}
		if pellet.Workspace != nil {
			view.Owner = fmt.Sprintf("workspace %d · %s", pellet.Workspace.ID, localPath(pellet.Workspace.RootPath))
			view.OwnerCurrent = current != nil && current.ID == pellet.Workspace.ID
		}
		views = append(views, view)
	}
	return views
}

func makeMemoryViews(memories []storage.Memory, code string, selected int64) []memoryView {
	views := make([]memoryView, 0, len(memories))
	for _, memory := range memories {
		views = append(views, memoryView{
			Memory: memory, Version: storage.MemoryVersion(memory), Selected: memory.ID == selected,
			URL: "/projects/" + url.PathEscape(code) + "/memories/" + strconv.FormatInt(memory.ID, 10),
		})
	}
	return views
}

func makeGroupViews(groups []*string, selected string) []groupView {
	views := []groupView{{Label: "All groups", Selected: selected == ""}}
	for _, group := range groups {
		value, label := encodeGroup(group), "Ungrouped"
		if group != nil {
			label = *group
		}
		views = append(views, groupView{Value: value, Label: label, Selected: selected == value})
	}
	return views
}

func parseFilters(values url.Values) (storage.WebPelletFilters, filterView, error) {
	var filters storage.WebPelletFilters
	view := filterView{Status: values.Get("status"), Group: values.Get("group"), ExternalID: values.Get("external_id"), Query: values.Get("q")}
	if len(view.Query) > 1024 || len(view.ExternalID) > 4096 || len(view.Group) > 8192 {
		return filters, view, domain.NewError(domain.Usage, "invalid_filter", "a web filter is too long", nil)
	}
	if view.Status != "" {
		status := domain.PelletStatus(view.Status)
		if err := domain.ValidatePelletStatus(status); err != nil {
			return filters, view, err
		}
		filters.Status = &status
	}
	if view.ExternalID != "" {
		value := view.ExternalID
		filters.ExternalID = &value
	}
	if view.Group != "" {
		group, err := decodeGroup(view.Group)
		if err != nil {
			return filters, view, domain.NewError(domain.Usage, "invalid_group_filter", "the group filter is invalid", nil)
		}
		filters.Group = storage.WebExactFilter{Set: true, Value: group}
	}
	filters.Query = view.Query
	return filters, view, nil
}

func encodeGroup(group *string) string {
	if group == nil {
		return "n"
	}
	return "v" + base64.RawURLEncoding.EncodeToString([]byte(*group))
}

func decodeGroup(encoded string) (*string, error) {
	if encoded == "n" {
		return nil, nil
	}
	if !strings.HasPrefix(encoded, "v") {
		return nil, errors.New("missing group encoding")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encoded, "v"))
	if err != nil || len(decoded) == 0 {
		return nil, errors.New("invalid group encoding")
	}
	value := string(decoded)
	return &value, nil
}

func taskURL(code string, values url.Values, reference string) string {
	path := "/projects/" + url.PathEscape(code) + "/tasks"
	if reference != "" {
		path += "/" + url.PathEscape(reference)
	}
	if len(values) == 0 {
		return path
	}
	copy := url.Values{}
	for _, key := range []string{"status", "group", "external_id", "q"} {
		if value := values.Get(key); value != "" {
			copy.Set(key, value)
		}
	}
	if query := copy.Encode(); query != "" {
		path += "?" + query
	}
	return path
}

func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func (h *handler) setCSRFCookie(response http.ResponseWriter) {
	http.SetCookie(response, &http.Cookie{
		Name: csrfCookieName, Value: h.config.CSRF, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: 0,
	})
}

func (h *handler) render(response http.ResponseWriter, status int, name string, data pageData) {
	var output bytes.Buffer
	if err := h.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(response, "could not render local interface", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_, _ = response.Write(output.Bytes())
}

func (h *handler) renderError(response http.ResponseWriter, status int, err error, draft map[string]string) {
	message := domain.PublicError(err).Message
	h.render(response, status, "error", pageData{Error: message, StatusCode: status, Conflict: &conflictView{Draft: draft}})
}

func requestError(message string) error {
	return domain.NewError(domain.Usage, "invalid_web_request", message, nil)
}

func statusForError(err error) int {
	var conflict *storage.OptimisticConflict
	if errors.As(err, &conflict) {
		return http.StatusConflict
	}
	public := domain.PublicError(err)
	switch public.Kind {
	case domain.Usage:
		return http.StatusUnprocessableEntity
	case domain.NotFound:
		return http.StatusNotFound
	case domain.Conflict, domain.Confirmation:
		return http.StatusConflict
	case domain.Storage:
		if public.Code == "database_busy" {
			return http.StatusServiceUnavailable
		}
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func statusLabel(value any) string {
	var status string
	switch typed := value.(type) {
	case domain.PelletStatus:
		status = string(typed)
	case storage.PelletLifecycleOperation:
		status = string(typed)
	case string:
		status = typed
	default:
		status = fmt.Sprint(typed)
	}
	words := strings.ReplaceAll(status, "_", " ")
	if words == "" {
		return ""
	}
	return strings.ToUpper(words[:1]) + words[1:]
}

func formatTime(value time.Time) string { return value.UTC().Format("2006-01-02 15:04:05Z") }
func nullableText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func textOrDash(value *string) string {
	if value == nil {
		return "—"
	}
	return *value
}
func localPath(value domain.LocalPath) string {
	if value.Relative {
		return "./" + value.Value
	}
	return value.Value
}

func constantEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
