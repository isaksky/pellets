package webui

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
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
	Stopping       <-chan struct{}
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
		"statusLabel":   statusLabel,
		"runStateLabel": runStateLabel,
		"formatTime":    formatTime,
		"relativeTime":  relativeTime,
		"filterCount": func(f filterView) int {
			n := 0
			if f.Status != "" {
				n++
			}
			if f.Group != "" {
				n++
			}
			if f.ExternalID != "" {
				n++
			}
			return n
		},
		"text":     nullableText,
		"path":     localPath,
		"eqStatus": func(left domain.PelletStatus, right string) bool { return string(left) == right },
		"sameID":   func(left, right int64) bool { return left == right },
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
	case request.Method == http.MethodGet && len(pathSegments(request.URL.Path)) == 4 && pathSegments(request.URL.Path)[2] == "schedules":
		h.readSchedule(response, request)
	case request.Method == http.MethodGet:
		if request.Header.Get("Datastar-Request") == "true" {
			response = &datastarResponse{ResponseWriter: response, request: request}
		}
		h.servePage(response, request)
	case request.Method == http.MethodPost:
		if request.Header.Get("Datastar-Request") == "true" {
			response = &datastarResponse{ResponseWriter: response, request: request}
		}
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
	response.Header().Set("Cache-Control", "no-cache")
	if name == "datastar-1.0.3.js" {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
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
		case <-h.config.Stopping:
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
	QueueOrderURL      string
	DatabaseLabel      string
	DatabasePath       string
	WorkspacesURL      string
	SelectedWorkspace  int64
	WorkspaceAttention int
	Nonce              string
	CSRF               string
	Projects           []projectView
	Project            storage.Project
	ProjectSummary     storage.WebProjectSummary
	HasProject         bool
	MultiProject       bool
	RunWorkspaces      []runWorkspaceView
	Area               string
	TasksURL           string
	MemoriesURL        string
	CurrentURL         string
	CloseURL           string
	Pellets            []pelletView
	Memories           []memoryView
	Groups             []groupView
	Filters            filterView
	SortHeaders        []sortHeaderView
	SelectedPellet     *pelletView
	SelectedMemory     *memoryView
	MoveTargets        []pelletView
	Flash              string
	Conflict           *conflictView
	Error              string
	StatusCode         int
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
	ID     int64
	Root   string
	GitDir string
	Pellet string
}

// runWorkspaceView combines a process-local schedule receipt with the latest
// durable attempt. The receipt is useful while this server is alive; the run
// remains authoritative after a browser reconnect or server restart.
type runWorkspaceView struct {
	Name              string
	URL               string
	ID                int64
	Root              string
	ActivePellet      string
	Run               *runView
	Schedule          *scheduleView
	ExternalID        string
	Group             string
	Busy              bool
	UngroupedFilter   bool
	RecoveryAttention string
	NoRunResume       *noRunResumeView
}

// Without a crash receipt, filters are suggestions from current metadata.
// A validated preflight receipt instead requires its exact saved intent.
type noRunResumeView struct {
	PelletNumber           int64
	ImplementationRevision int64
	ExternalID             string
	Group                  string
	SavedMode              string
	SavedLimit             int
	ReceiptToken           string
}

type runView struct {
	ID            int64
	Revision      int64
	Pellet        string
	Mode          string
	Model         string
	Effort        string
	Phase         string
	State         string
	Activity      string
	Outcome       string
	Commit        string
	Error         string
	ExternalID    string
	Group         string
	PelletNumber  int64
	CanResume     bool
	UnownedActive bool
	ResumeMode    string
	ResumeLimit   int
	Awaiting      bool
	AutoReview    bool
	Interrupted   bool
	Attention     bool
	Active        bool
	Interaction   *storage.RunInteraction
}

type scheduleView struct {
	ID         int64
	Mode       string
	State      string
	StopAfter  bool
	ExternalID string
	Group      string
}

type pelletView struct {
	CheckpointOutcome *checkpointOutcomeView
	Pellet            storage.Pellet
	Version           string
	URL               string
	Selected          bool
	Group             string
	ExternalID        string
	Priority          string
	Owner             string
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
	Status         string
	Group          string
	ExternalID     string
	Query          string
	Sort           string
	Direction      string
	GroupText      string
	GroupUngrouped bool
	GroupExact     bool
}

type sortHeaderView struct {
	Key         string
	Label       string
	URL         string
	ActionLabel string
	AriaSort    string
	Indicator   string
	Active      bool
	TitleColumn bool
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
	if area != "tasks" && area != "memories" && area != "workspaces" || len(segments) > 4 {
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
	if request.Header.Get("Datastar-Request") == "true" {
		switch request.Header.Get("Pellets-Target") {
		case "live":
			templateName = "live"
		case "tasks-area":
			templateName = "tasks-area"
		case "task-list":
			templateName = "task-list"
		case "memory-list":
			templateName = "memory-list"
		case "project-drawer":
			templateName = "project-rail"
		case "run-dashboard":
			templateName = "run-dashboard"
		case "project-record":
			templateName = "project-record"
		case "inspector-host":
			templateName = "inspector"
		default:
			http.Error(response, "invalid fragment target", http.StatusBadRequest)
			return
		}
	}
	h.render(response, http.StatusOK, templateName, data)
}

func (h *handler) serveRoot(response http.ResponseWriter, request *http.Request) {
	stream := request.Header.Get("Datastar-Request") == "true"
	if stream && request.Header.Get("Pellets-Target") != "live" {
		http.Error(response, "invalid fragment target", http.StatusBadRequest)
		return
	}
	projects, err := h.application.Projects(request.Context())
	if err != nil {
		h.renderError(response, statusForError(err), err, nil)
		return
	}
	if len(projects) == 0 {
		h.setCSRFCookie(response)
		name := "page"
		if stream {
			name = "app-content"
		}
		h.render(response, http.StatusOK, name, pageData{CSRF: h.config.CSRF, DatabasePath: h.application.Database.Path, CurrentURL: "/"})
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
	path := "/projects/" + url.PathEscape(code) + "/tasks"
	if stream {
		// Bootstrap the regions absent from the empty page in one Datastar morph.
		pageRequest := request.Clone(request.Context())
		pageRequest.URL.Path, pageRequest.URL.RawQuery = path, ""
		data, err := h.loadPage(pageRequest, code, "tasks", pathSegments(path))
		if err != nil {
			h.renderError(response, statusForError(err), err, nil)
			return
		}
		h.setCSRFCookie(response)
		h.render(response, http.StatusOK, "app-content", data)
		return
	}
	http.Redirect(response, request, path, http.StatusSeeOther)
}

func (h *handler) loadPage(request *http.Request, code, area string, segments []string) (pageData, error) {
	// Datastar's transport signal parameter is not part of a navigable page URL.
	pageURL := *request.URL
	query := pageURL.Query()
	query.Del("datastar")
	pageURL.RawQuery = query.Encode()
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
		DatabaseLabel: h.application.Database.Root, DatabasePath: h.application.Database.Path, WorkspacesURL: "/projects/" + url.PathEscape(code) + "/workspaces",
		CSRF: h.config.CSRF, Project: selected.Project, ProjectSummary: selected, HasProject: true,
		MultiProject: len(projects) > 1, Area: area,
		TasksURL:    "/projects/" + url.PathEscape(code) + "/tasks",
		MemoriesURL: "/projects/" + url.PathEscape(code) + "/memories",
		CurrentURL:  pageURL.RequestURI(),
	}
	data.Projects, err = h.projectViews(request, projects, code, area)
	if err != nil {
		return pageData{}, err
	}
	data.RunWorkspaces, err = h.runWorkspaceViews(request, selected.Project)
	if err != nil {
		return pageData{}, err
	}
	for _, workspace := range data.RunWorkspaces {
		if workspace.RecoveryAttention != "" || (workspace.Run != nil && (workspace.Run.Attention || workspace.Run.Awaiting || workspace.Run.Interrupted || workspace.Run.UnownedActive)) {
			data.WorkspaceAttention++
		}
	}
	if area == "workspaces" {
		_, filters, filterErr := parseFilters(request.URL.Query())
		if filterErr != nil {
			return pageData{}, filterErr
		}
		for i := range data.RunWorkspaces {
			data.RunWorkspaces[i].ExternalID = filters.ExternalID
			data.RunWorkspaces[i].Group = filters.GroupText
			data.RunWorkspaces[i].UngroupedFilter = filters.GroupUngrouped
		}
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
		for index := range data.RunWorkspaces {
			data.RunWorkspaces[index].ExternalID = view.ExternalID
			data.RunWorkspaces[index].Group = view.GroupText
			data.RunWorkspaces[index].UngroupedFilter = view.GroupUngrouped
		}
		data.TasksURL = taskURL(code, nil, "", filters.Sort)
		data.QueueOrderURL = taskURL(code, request.URL.Query(), selectedReferenceText, storage.WebPelletSort{})
		data.CurrentURL = taskURL(code, request.URL.Query(), selectedReferenceText, filters.Sort)
		data.SortHeaders = makeSortHeaderViews(code, request.URL.Query(), selectedReferenceText, filters.Sort)
		pellets, err := h.application.Pellets(request.Context(), selected.Project, filters)
		if err != nil {
			return pageData{}, err
		}
		data.Pellets = makePelletViews(pellets, selected.Project.Code, request.URL.Query(), selectedReferenceText, filters.Sort)
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
			views := makePelletViews([]storage.Pellet{pellet}, code, request.URL.Query(), selectedReferenceText, filters.Sort)
			data.SelectedPellet = &views[0]
			if pellet.Checkpoint != nil {
				outcome, err := h.application.CheckpointOutcome(request.Context(), pellet)
				if err != nil {
					return pageData{}, err
				}
				data.SelectedPellet.CheckpointOutcome = makeCheckpointOutcomeView(outcome, code, request.URL.Query(), filters.Sort)
			}
			data.CloseURL = taskURL(code, request.URL.Query(), "", filters.Sort)
			active, err := h.application.Pellets(request.Context(), selected.Project, storage.WebPelletFilters{})
			if err != nil {
				return pageData{}, err
			}
			for _, candidate := range active {
				if candidate.Reference != selectedReference && (candidate.Status == domain.PelletOpen || candidate.Status == domain.PelletInProgress) {
					data.MoveTargets = append(data.MoveTargets, makePelletViews([]storage.Pellet{candidate}, code, nil, "", filters.Sort)[0])
				}
			}
		}
	} else if area == "memories" {
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
	if area == "workspaces" && len(segments) == 4 {
		id, parseErr := strconv.ParseInt(segments[3], 10, 64)
		if parseErr != nil || !slices.ContainsFunc(data.RunWorkspaces, func(w runWorkspaceView) bool { return w.ID == id }) {
			return pageData{}, domain.NewError(domain.NotFound, "workspace_not_registered", "the workspace does not belong to this project", nil)
		}
		data.SelectedWorkspace = id
	}
	return data, nil
}

func (h *handler) runWorkspaceViews(request *http.Request, project storage.Project) ([]runWorkspaceView, error) {
	views := make([]runWorkspaceView, 0, len(project.Workspaces))
	status := domain.PelletInProgress
	inProgress, err := h.application.Pellets(request.Context(), project, storage.WebPelletFilters{Status: &status})
	if err != nil {
		return nil, err
	}
	owners := make(map[int64]storage.Pellet, len(inProgress))
	for _, pellet := range inProgress {
		if pellet.Workspace != nil {
			owners[pellet.Workspace.ID] = pellet
		}
	}
	for _, workspace := range project.Workspaces {
		root := workspace.RootPath.Value
		if workspace.RootPath.Relative {
			root = filepath.Join(h.application.Database.Root, root)
		}
		name := filepath.Base(filepath.Clean(root))
		view := runWorkspaceView{ID: workspace.ID, Root: localPath(workspace.RootPath), Name: name,
			URL: "/projects/" + url.PathEscape(project.Code) + "/workspaces/" + strconv.FormatInt(workspace.ID, 10)}
		owned, hasOwned := owners[workspace.ID]
		if hasOwned {
			view.ActivePellet = owned.Reference.String()
		}
		runs, err := h.application.WorkspaceRuns(request.Context(), workspace.ID)
		if err != nil {
			return nil, err
		}
		if len(runs) > 0 {
			run := makeRunView(runs[0])
			if storage.RunActive(runs[0].State) && h.application.Executions != nil && !h.application.Executions.OwnsRun(h.application.Database, runs[0].ID) {
				run.UnownedActive, run.CanResume = true, runs[0].PelletPresent
			}
			if runs[0].State == "completed" && h.application.Executions != nil && h.application.Executions.RecoveryPending(h.application.Database, runs[0]) {
				run.CanResume = runs[0].PelletPresent
			}
			view.Run = &run
			view.Busy = storage.RunActive(runs[0].State)
		}
		if h.application.Scheduler != nil {
			view.RecoveryAttention = h.application.Scheduler.WorkspaceAttention(workspace.ID)
			if schedule, ok := h.application.Scheduler.WorkspaceStatus(workspace.ID); ok {
				view.Schedule = &scheduleView{ID: schedule.ID, Mode: schedule.Mode, State: schedule.State, StopAfter: schedule.StopAfterPellet, ExternalID: textOrDash(schedule.ExternalID), Group: textOrDash(schedule.Group)}
				view.Busy = true
			}
		}
		if hasOwned {
			currentGenerationRun := len(runs) > 0 && storage.RunMatchesPelletGeneration(runs[0], owned)
			if view.Run != nil && !currentGenerationRun {
				view.Run.CanResume = false
			}
			// A previous pellet's terminal run must not hide a newly CLI-started
			// pellet or a reopened generation whose preflight failed before capture.
			if !view.Busy && !currentGenerationRun {
				view.NoRunResume = &noRunResumeView{PelletNumber: owned.Reference.Number, ImplementationRevision: owned.ImplementationRevision}
				if owned.ExternalID != nil {
					view.NoRunResume.ExternalID = *owned.ExternalID
				}
				if owned.Group != nil {
					view.NoRunResume.Group = *owned.Group
				}
				if h.application.Executions != nil {
					receipt, err := h.application.Executions.PreflightRecovery(request.Context(), h.application.Database, storage.ResolvedProject{Project: project, Workspace: workspace}, owned)
					if err != nil {
						view.NoRunResume = nil
						view.RecoveryAttention = domain.PublicError(err).Message
					} else if receipt != nil {
						view.NoRunResume.SavedMode, view.NoRunResume.SavedLimit = receipt.ScheduleMode, receipt.ScheduleRemaining
						view.NoRunResume.ReceiptToken = receipt.Token()
						view.NoRunResume.ExternalID, view.NoRunResume.Group = "", ""
						if receipt.ExternalID != nil {
							view.NoRunResume.ExternalID = *receipt.ExternalID
						}
						if receipt.Group != nil {
							view.NoRunResume.Group = *receipt.Group
						}
					}
				}
			}
			// Ordinary queue controls cannot resume an owned pellet.
			view.Busy = true
		}
		views = append(views, view)
	}
	return views, nil
}

func makeRunView(run storage.ExecutionRun) runView {
	model, effort := run.Settings.Codex.Model, run.Settings.Codex.ReasoningEffort
	if model == "" {
		model = "runtime default"
	}
	if effort == "" {
		effort = "default"
	}
	activity := run.Summary
	if activity == "" && len(run.Activity) > 0 {
		activity = run.Activity[0].Summary
	}
	if activity == "" {
		activity = "No activity reported yet."
	}
	activity = publicRunActivity(activity)
	view := runView{ID: run.ID, Revision: run.Revision, Pellet: run.ProjectCode + "-" + strconv.FormatInt(run.PelletNumber, 10), PelletNumber: run.PelletNumber, Mode: run.Mode, Model: model, Effort: effort, Phase: run.Phase, State: run.State, Activity: activity, Commit: run.ResultCommit, Error: run.ErrorCode, ExternalID: textOrDash(run.ExternalID), Group: textOrDash(run.Group), Active: storage.RunActive(run.State), Interaction: run.Interaction}
	view.Outcome = run.Outcome
	view.Awaiting = run.State == "awaiting_input" || run.ErrorCode == "codex_input_required"
	view.Interrupted = run.State == "interrupted"
	view.Attention = run.State == "needs_attention"
	view.CanResume = !storage.RunActive(run.State) && run.State != "completed" && run.PelletPresent
	view.ResumeMode, view.ResumeLimit = run.ScheduleMode, run.ScheduleRemaining
	lowerActivity := strings.ToLower(activity)
	view.AutoReview = strings.Contains(lowerActivity, "automatic approval review is in progress") || strings.Contains(lowerActivity, "automatic approval review requires attention") || strings.Contains(lowerActivity, "explicit human decision")
	return view
}

func publicRunActivity(activity string) string {
	// Git diagnostics are durable evidence for the supervisor, but a browser
	// activity card must never become a raw-command-output surface. Codex event
	// summaries are already fixed strings; this protects the remaining local
	// diagnostic path as well.
	if strings.HasPrefix(activity, "Git:") {
		return "Repository verification needs attention; inspect the local server diagnostics."
	}
	return activity
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
			view.Workspaces = append(view.Workspaces, workspaceView{
				ID: workspace.ID, Root: localPath(workspace.RootPath), GitDir: localPath(workspace.GitDir),
				Pellet: owners[workspace.ID],
			})
		}
		views = append(views, view)
	}
	return views, nil
}

func makePelletViews(pellets []storage.Pellet, code string, query url.Values, selected string, sort storage.WebPelletSort) []pelletView {
	views := make([]pelletView, 0, len(pellets))
	for _, pellet := range pellets {
		view := pelletView{
			Pellet: pellet, Version: storage.PelletVersion(pellet),
			URL: taskURL(code, query, pellet.Reference.String(), sort), Selected: pellet.Reference.String() == selected,
			Group: textOrDash(pellet.Group), ExternalID: textOrDash(pellet.ExternalID), Priority: "—",
		}
		if pellet.Priority != nil {
			view.Priority = strconv.FormatInt(*pellet.Priority, 10)
		}
		if pellet.Workspace != nil {
			view.Owner = fmt.Sprintf("workspace %d · %s", pellet.Workspace.ID, localPath(pellet.Workspace.RootPath))
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
		view.GroupExact = true
		if group != nil {
			view.GroupText = *group
		} else {
			view.GroupUngrouped = true
		}
	}
	filters.Query = view.Query
	filters.Sort = storage.NormalizeWebPelletSort(storage.WebPelletSort{
		Column:    storage.WebPelletSortColumn(values.Get("sort")),
		Direction: storage.WebPelletSortDirection(values.Get("direction")),
	})
	view.Sort = string(filters.Sort.Column)
	view.Direction = string(filters.Sort.Direction)
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

func makeSortHeaderViews(code string, values url.Values, reference string, requested storage.WebPelletSort) []sortHeaderView {
	active := storage.NormalizeWebPelletSort(requested)
	columns := []struct {
		column storage.WebPelletSortColumn
		label  string
	}{
		{storage.WebPelletSortReference, "Reference"},
		{storage.WebPelletSortTitle, "Title"},
		{storage.WebPelletSortGroup, "Group"},
		{storage.WebPelletSortStatus, "Status"},
		{storage.WebPelletSortPriority, "Priority"},
		{storage.WebPelletSortExternalID, "External ID"},
		{storage.WebPelletSortUpdated, "Updated"},
	}
	views := make([]sortHeaderView, 0, len(columns))
	for _, definition := range columns {
		direction := storage.WebPelletSortAscending
		isActive := definition.column == active.Column
		ariaSort, indicator := "", ""
		if isActive {
			if active.Direction == storage.WebPelletSortAscending {
				direction, ariaSort, indicator = storage.WebPelletSortDescending, "ascending", "↑"
			} else {
				direction, ariaSort, indicator = storage.WebPelletSortAscending, "descending", "↓"
			}
		}
		next := storage.WebPelletSort{Column: definition.column, Direction: direction}
		views = append(views, sortHeaderView{
			Key: string(definition.column), Label: definition.label, URL: taskURL(code, values, reference, next),
			ActionLabel: "Sort by " + definition.label + " " + sortDirectionLabel(direction),
			AriaSort:    ariaSort, Indicator: indicator, Active: isActive,
			TitleColumn: definition.column == storage.WebPelletSortTitle,
		})
	}
	return views
}

func sortDirectionLabel(direction storage.WebPelletSortDirection) string {
	if direction == storage.WebPelletSortDescending {
		return "descending"
	}
	return "ascending"
}

func taskURL(code string, values url.Values, reference string, requested storage.WebPelletSort) string {
	path := "/projects/" + url.PathEscape(code) + "/tasks"
	if reference != "" {
		path += "/" + url.PathEscape(reference)
	}
	copy := url.Values{}
	for _, key := range []string{"status", "group", "external_id", "q"} {
		if value := values.Get(key); value != "" {
			copy.Set(key, value)
		}
	}
	sort := storage.NormalizeWebPelletSort(requested)
	copy.Set("sort", string(sort.Column))
	copy.Set("direction", string(sort.Direction))
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
	if name == "page" {
		var nonce [24]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			http.Error(response, "could not secure local interface", http.StatusInternalServerError)
			return
		}
		data.Nonce = base64.RawStdEncoding.EncodeToString(nonce[:])
		policy := response.Header().Get("Content-Security-Policy")
		response.Header().Set("Content-Security-Policy", strings.Replace(policy, "script-src 'self'", "script-src 'self' 'nonce-"+data.Nonce+"'", 1))
	}
	if stream, ok := response.(*datastarResponse); ok && name != "page" && status < 400 {
		h.renderUpdates(stream, status, name, data)
		return
	}
	var output bytes.Buffer
	if err := h.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(response, "could not render local interface", http.StatusInternalServerError)
		return
	}
	if stream, ok := response.(*datastarResponse); ok && name != "page" {
		stream.render(status, name, data.CurrentURL, output.String())
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

func runStateLabel(state string) string {
	switch state {
	case "awaiting_input":
		return "Awaiting input"
	case "needs_attention":
		return "Needs attention"
	case "interrupted":
		return "Interrupted"
	case "completed":
		return "Completed"
	case "running":
		return "Running"
	default:
		return statusLabel(state)
	}
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

// Render all related regions from one materialized page, before writing any SSE.
func (h *handler) renderUpdates(response *datastarResponse, status int, primary string, data pageData) {
	names := []string{}
	if primary != "live" {
		names = append(names, primary)
	}
	names = append(names, "project-counts", "area-tabs", "project-record", "project-rail")
	if data.Area == "workspaces" {
		names = append(names, "run-dashboard")
	}
	if data.Area == "tasks" {
		names = append(names, "filter-summary", "queue-order")
	}
	if data.Area == "tasks" && primary != "tasks-area" && primary != "task-list" {
		names = append(names, "task-list")
	}
	if data.Area == "memories" && primary != "memory-list" {
		names = append(names, "memory-list")
	}
	if primary == "live" {
		names = append(names, "inspector")
	}
	if primary == "app-content" {
		names = []string{"app-content"}
	}
	type patch struct{ selector, mode, html string }
	patches := []patch{}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		var output bytes.Buffer
		if err := h.templates.ExecuteTemplate(&output, name, data); err != nil {
			http.Error(response, "could not render local interface", http.StatusInternalServerError)
			return
		}
		selector, mode := "#"+name, "outer"
		if name == "project-rail" {
			selector = "#project-drawer"
		}
		if name == "inspector" {
			selector, mode = "#inspector-host", "inner"
		}
		patches = append(patches, patch{selector, mode, output.String()})
	}
	response.start()
	for _, patch := range patches {
		response.patch(patch.selector, patch.mode, patch.html)
	}
	response.result(status, data.CurrentURL)
}

func relativeTime(value time.Time) string {
	age := time.Since(value)
	switch {
	case age < time.Minute:
		return "Just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	case age < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	default:
		return value.Format("Jan 2, 2006")
	}
}
