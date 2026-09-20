package webui

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type assignmentGroupView struct {
	Value, Label string
	Assigned     bool
}

type routingCategoryView struct {
	Category, Label, Description, Project, Version, CSRF, ReturnURL string
	Automatic                                                       bool
	Recipients                                                      []routingRecipientView
}
type routingRecipientView struct {
	ID                        int64
	Name, Root                string
	Chosen, Main, Unavailable bool
}

func statusSymbol(value domain.PelletStatus) string {
	switch value {
	case domain.PelletInProgress:
		return "◐"
	case domain.PelletClosed:
		return "●"
	case domain.PelletMaybeLater:
		return "◌"
	default:
		return "○"
	}
}

// Workspace selection routes browsing, independently of scheduling controls.
// Checkpoint membership comes only from persisted explicit target identities.
func (h *handler) prepareWorkbench(request *http.Request, data *pageData) error {
	routing, err := h.application.Routing(request.Context(), data.Project)
	if err != nil {
		return err
	}
	data.Routing = routing
	for _, key := range []string{"workspace", "execution"} {
		raw := request.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		found := false
		for _, w := range data.RunWorkspaces {
			if w.ID == id {
				found = true
			}
		}
		if err != nil || !found {
			return domain.NewError(domain.NotFound, "workspace_not_registered", "the workspace does not belong to this project", nil)
		}
		if key == "workspace" {
			data.SelectedWorkspace = id
		} else {
			data.ExecutionWorkspace = id
		}
	}
	if data.SelectedWorkspace != 0 {
		data.ExecutionWorkspace = data.SelectedWorkspace
	}
	if data.ExecutionWorkspace == 0 && len(data.RunWorkspaces) > 0 {
		data.ExecutionWorkspace = data.RunWorkspaces[0].ID
		if h.application.Current != nil && h.application.Current.Project.ID == data.Project.ID {
			data.ExecutionWorkspace = h.application.Current.Workspace.ID
		}
	}
	clearQuery := url.Values{}
	if data.SelectedWorkspace != 0 {
		clearQuery.Set("workspace", strconv.FormatInt(data.SelectedWorkspace, 10))
	}
	if data.ExecutionWorkspace != 0 {
		clearQuery.Set("execution", strconv.FormatInt(data.ExecutionWorkspace, 10))
	}
	data.ClearFiltersURL = taskURL(data.Project.Code, clearQuery, "", storage.WebPelletSort{Column: storage.WebPelletSortColumn(data.Filters.Sort), Direction: storage.WebPelletSortDirection(data.Filters.Direction)})
	if data.ExecutionWorkspace != 0 {
		executionQuery := url.Values{"execution": {strconv.FormatInt(data.ExecutionWorkspace, 10)}}
		data.TasksURL = taskURL(data.Project.Code, executionQuery, "", storage.WebPelletSort{Column: storage.WebPelletSortColumn(data.Filters.Sort), Direction: storage.WebPelletSortDirection(data.Filters.Direction)})
		data.GroupsURL = "/projects/" + url.PathEscape(data.Project.Code) + "/groups?" + executionQuery.Encode()
		data.MemoriesURL = "/projects/" + url.PathEscape(data.Project.Code) + "/memories?" + executionQuery.Encode()
		for i := range data.Memories {
			data.Memories[i].URL += "?" + executionQuery.Encode()
		}
		if data.SelectedMemory != nil {
			data.SelectedMemory.URL += "?" + executionQuery.Encode()
			data.CloseURL = data.MemoriesURL
		}
	}
	if data.SelectedPellet != nil {
		workspace := data.ExecutionWorkspace
		if data.SelectedPellet.Pellet.Workspace != nil {
			workspace = data.SelectedPellet.Pellet.Workspace.ID
		}
		data.SelectedPellet.ExecutionURL = taskURL(data.Project.Code, url.Values{"workspace": {strconv.FormatInt(workspace, 10)}}, "", storage.WebPelletSort{})
	}
	for i := range data.RunWorkspaces {
		w := &data.RunWorkspaces[i]
		w.URL = data.TasksURL + "?workspace=" + strconv.FormatInt(w.ID, 10)
		// TasksURL may include normalized sort settings.
		u, _ := url.Parse(data.TasksURL)
		q := u.Query()
		q.Set("workspace", strconv.FormatInt(w.ID, 10))
		u.RawQuery = q.Encode()
		w.URL = u.String()
		if w.ID == data.SelectedWorkspace {
			data.WorkspaceName, data.WorkspaceRoot = w.Name, w.Root
		}
	}
	data.Assignment = routing.Assignment(data.SelectedWorkspace)
	data.EffectiveAssignment = routing.EffectiveAssignment(data.SelectedWorkspace)
	for _, category := range []routingCategoryView{
		{Category: "remaining", Label: "All other groups", Description: "Named groups not explicitly assigned to another workspace.", Automatic: data.EffectiveAssignment.AutomaticRemaining},
		{Category: "ungrouped", Label: "Ungrouped", Description: "Pellets with no group.", Automatic: data.EffectiveAssignment.AutomaticUngrouped},
	} {
		if category.Category == "remaining" && data.EffectiveAssignment.Mode != "remaining" || category.Category == "ungrouped" && !data.EffectiveAssignment.IncludeUngrouped {
			continue
		}
		category.Project, category.Version, category.CSRF, category.ReturnURL = data.Project.Code, routing.Version, data.CSRF, data.CurrentURL
		for _, w := range data.RunWorkspaces {
			a := routing.Assignment(w.ID)
			chosen := a.Mode == "remaining"
			if category.Category == "ungrouped" {
				chosen = a.IncludeUngrouped
			}
			category.Recipients = append(category.Recipients, routingRecipientView{ID: w.ID, Name: w.Name, Root: w.Root, Chosen: chosen, Main: w.ID == routing.MainWorkspaceID, Unavailable: routing.EffectiveAssignment(w.ID).Unavailable})
		}
		data.RoutingCategories = append(data.RoutingCategories, category)
	}
	clearGroup := *request.URL
	groupQuery := clearGroup.Query()
	groupQuery.Del("group")
	groupQuery.Del("datastar")
	clearGroup.RawQuery = groupQuery.Encode()
	data.ClearGroupURL = clearGroup.RequestURI()
	all, err := h.application.Pellets(request.Context(), data.Project, storage.WebPelletFilters{Sort: storage.WebPelletSort{Column: storage.WebPelletSortColumn(data.Filters.Sort), Direction: storage.WebPelletSortDirection(data.Filters.Direction)}})
	if err != nil {
		return err
	}
	for _, p := range all {
		if p.Kind == "ordinary" && (p.Status == domain.PelletOpen || p.Status == domain.PelletInProgress) {
			data.ProjectQueueCount++
		}
		if (p.Status == domain.PelletOpen || p.Status == domain.PelletInProgress) && (p.Checkpoint == nil || !p.Checkpoint.Removed) {
			data.QueueContext = append(data.QueueContext, makePelletViews([]storage.Pellet{p}, data.Project.Code, request.URL.Query(), "", storage.WebPelletSort{})[0])
		}
		if p.Kind == "ordinary" {
			data.ScopeCandidates = append(data.ScopeCandidates, makePelletViews([]storage.Pellet{p}, data.Project.Code, request.URL.Query(), "", storage.WebPelletSort{})[0])
		}
	}
	sort.SliceStable(data.QueueContext, func(i, j int) bool {
		a, b := data.QueueContext[i].Pellet, data.QueueContext[j].Pellet
		if a.Priority == nil {
			return false
		}
		if b.Priority == nil {
			return true
		}
		if *a.Priority == *b.Priority {
			return a.Reference.Number < b.Reference.Number
		}
		return *a.Priority < *b.Priority
	})
	groupSet := map[string]bool{}
	for _, p := range all {
		if p.Group != nil {
			groupSet[*p.Group] = true
		}
	}
	for _, a := range routing.Assignments {
		for _, g := range a.Groups {
			groupSet[g] = true
		}
	}
	for g := range groupSet {
		assigned := false
		for _, saved := range data.Assignment.Groups {
			if saved == g {
				assigned = true
			}
		}
		data.AssignmentGroups = append(data.AssignmentGroups, assignmentGroupView{Value: encodeGroup(&g), Label: g, Assigned: assigned})
	}
	sortAssignmentGroups(data.AssignmentGroups)
	if data.Area != "tasks" {
		return nil
	}
	current := make(map[int64]storage.Pellet, len(all))
	order := make(map[int64]int, len(all))
	for i, p := range all {
		current[p.Reference.Number] = p
		order[p.Reference.Number] = i
	}
	visible := map[string]bool{}
	ordinary := map[string]bool{}
	for _, p := range data.Pellets {
		owned := p.Pellet.Workspace != nil && p.Pellet.Workspace.ID == data.SelectedWorkspace
		if workbenchStatusMatches(data.Filters.Status, p.Pellet.Status) && p.Pellet.Kind == "ordinary" && (data.SelectedWorkspace == 0 || owned || routing.Accepts(data.SelectedWorkspace, p.Pellet)) {
			visible[p.Pellet.Reference.String()] = true
			ordinary[p.Pellet.Reference.String()] = true
		}
	}
	selected := data.Pellets[:0]
	for _, p := range data.Pellets {
		if !workbenchStatusMatches(data.Filters.Status, p.Pellet.Status) || p.Pellet.Checkpoint != nil && p.Pellet.Checkpoint.Removed && data.Filters.Status != "maybe_later" {
			continue
		}
		if p.Pellet.Kind == "ordinary" {
			if visible[p.Pellet.Reference.String()] {
				selected = append(selected, p)
			}
			continue
		}
		if p.Pellet.Checkpoint != nil {
			for _, target := range p.Pellet.Checkpoint.Targets {
				if ordinary[target.Reference] {
					visible[p.Pellet.Reference.String()] = true
				}
			}
		}
		if data.SelectedWorkspace == 0 || visible[p.Pellet.Reference.String()] || checkpointInWorkspace(routing, data.SelectedWorkspace, p.Pellet, current) {
			selected = append(selected, p)
		}
	}
	// A metadata filter must not hide a checkpoint whose exact scope intersects
	// visible ordinary work. Status filtering continues to apply to checkpoints.
	existing := map[string]bool{}
	for _, p := range selected {
		existing[p.Pellet.Reference.String()] = true
	}
	for _, p := range all {
		if p.Checkpoint == nil || (p.Checkpoint.Removed && data.Filters.Status != "maybe_later") || existing[p.Reference.String()] {
			continue
		}
		if !workbenchStatusMatches(data.Filters.Status, p.Status) {
			continue
		}
		intersects := false
		for _, t := range p.Checkpoint.Targets {
			if ordinary[t.Reference] {
				intersects = true
			}
		}
		if intersects {
			selected = append(selected, makePelletViews([]storage.Pellet{p}, data.Project.Code, request.URL.Query(), "", storage.WebPelletSort{})[0])
		}
	}
	data.Pellets = selected
	sort.SliceStable(data.Pellets, func(i, j int) bool {
		return order[data.Pellets[i].Pellet.Reference.Number] < order[data.Pellets[j].Pellet.Reference.Number]
	})
	for i := range data.Pellets {
		p := &data.Pellets[i]
		if p.Pellet.Workspace != nil {
			for _, workspace := range data.RunWorkspaces {
				if workspace.ID == p.Pellet.Workspace.ID {
					p.OwnerName = workspace.Name
					break
				}
			}
		}
		if p.Pellet.Checkpoint == nil {
			continue
		}
		refs := []string{}
		for _, target := range p.Pellet.Checkpoint.Targets {
			refs = append(refs, target.Reference)
			if ordinary[target.Reference] {
				p.ScopeVisible++
			}
		}
		p.ScopeRefs = strings.Join(refs, " ")
		p.ScopeTotal = len(refs)
	}
	return nil
}

func (h *handler) saveRouting(w http.ResponseWriter, r *http.Request, project storage.Project, workspace string) {
	var err error
	if workspace == "" && r.PostForm.Has("category") {
		var recipients []int64
		for _, raw := range r.PostForm["recipients"] {
			id, parseErr := strconv.ParseInt(raw, 10, 64)
			if parseErr != nil {
				h.renderError(w, 422, requestError("choose a registered workspace"), nil)
				return
			}
			recipients = append(recipients, id)
		}
		_, err = h.application.SaveRoutingRecipients(r.Context(), project, r.PostForm.Get("version"), r.PostForm.Get("category"), recipients)
	} else if workspace == "" {
		_, err = h.application.SetGroupAssignments(r.Context(), project, r.PostForm.Get("version"), r.PostForm.Get("enabled") == "true")
	} else {
		id, parseErr := strconv.ParseInt(workspace, 10, 64)
		if parseErr != nil {
			h.renderError(w, 422, requestError("choose a registered workspace"), submittedDraft(r.PostForm))
			return
		}
		groups := []string{}
		for _, encoded := range r.PostForm["groups"] {
			group, decodeErr := decodeGroup(encoded)
			if decodeErr != nil || group == nil {
				h.renderError(w, 422, requestError("choose a valid exact group assignment"), submittedDraft(r.PostForm))
				return
			}
			groups = append(groups, *group)
		}
		if value := r.PostForm.Get("new_group"); value != "" {
			groups = append(groups, value)
		}
		_, err = h.application.SaveWorkspaceAssignment(r.Context(), project, id, r.PostForm.Get("version"), storage.WorkspaceAssignment{WorkspaceID: id, Mode: r.PostForm.Get("mode"), Groups: groups, IncludeUngrouped: r.PostForm.Get("include_ungrouped") == "true"})
	}
	if err != nil {
		h.renderError(w, statusForError(err), err, submittedDraft(r.PostForm))
		return
	}
	h.hub.broadcast()
	// Mutations return an authoritative layout bundle; no browser routing state is
	// trusted for selection or ownership.
	pageRequest := r.Clone(r.Context())
	pageURL := *r.URL
	pageURL.Path = "/projects/" + url.PathEscape(project.Code) + "/tasks"
	if target, parseErr := url.Parse(r.PostForm.Get("return_to")); parseErr == nil && target.Scheme == "" && target.Host == "" {
		parts := pathSegments(target.Path)
		if len(parts) >= 3 && len(parts) <= 4 && parts[0] == "projects" && parts[1] == project.Code && (parts[2] == "tasks" || parts[2] == "memories") {
			pageURL.Path, pageURL.RawQuery = target.Path, target.RawQuery
		}
	}
	if workspace != "" {
		q := pageURL.Query()
		q.Set("workspace", workspace)
		pageURL.RawQuery = q.Encode()
	}
	pageRequest.URL = &pageURL
	data, err := h.loadPage(pageRequest, project.Code, pathSegments(pageURL.Path)[2], pathSegments(pageURL.Path))
	if err != nil {
		h.renderError(w, statusForError(err), err, nil)
		return
	}
	if _, ok := w.(*datastarResponse); ok {
		h.render(w, 200, "app-content", data)
	} else {
		http.Redirect(w, r, data.CurrentURL, http.StatusSeeOther)
	}
}

func workbenchStatusMatches(filter string, status domain.PelletStatus) bool {
	switch filter {
	case "", "active":
		return status == domain.PelletOpen || status == domain.PelletInProgress
	case "all":
		return true
	default:
		return string(status) == filter
	}
}

// Use live target metadata for browsing without mutating the checkpoint's
// captured review scope. A missing target keeps its saved group for diagnosis.
func checkpointInWorkspace(routing storage.ProjectRouting, workspaceID int64, checkpoint storage.Pellet, current map[int64]storage.Pellet) bool {
	if checkpoint.Workspace != nil && checkpoint.Workspace.ID == workspaceID {
		return true
	}
	if checkpoint.Checkpoint == nil {
		return false
	}
	for _, target := range checkpoint.Checkpoint.Targets {
		if pellet, ok := current[target.Number]; ok {
			if routing.Accepts(workspaceID, pellet) {
				return true
			}
		} else if routing.Selection(workspaceID).AcceptsGroup(target.Group) {
			return true
		}
	}
	return false
}
