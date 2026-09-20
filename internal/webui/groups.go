package webui

import (
	"net/http"
	"net/url"
	"strconv"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// Group URLs use stable identity; names remain exact editable project data.
type groupRecordView struct {
	Group                     storage.Group
	URL, RenameURL, FilterURL string
	Members                   []pelletView
}

func (h *handler) loadGroups(r *http.Request, data *pageData, segments []string) error {
	groups, err := h.application.ListGroups(r.Context(), data.Project)
	if err != nil {
		return err
	}
	pellets, err := h.application.Pellets(r.Context(), data.Project, storage.WebPelletFilters{})
	if err != nil {
		return err
	}
	query := url.Values{}
	if execution := r.URL.Query().Get("execution"); execution != "" {
		query.Set("execution", execution)
	}
	base := "/projects/" + url.PathEscape(data.Project.Code) + "/groups"
	data.GroupsURL = base
	suffix := ""
	if len(query) > 0 {
		suffix = "?" + query.Encode()
		data.GroupsURL += suffix
	}
	data.CloseURL = data.GroupsURL
	for _, group := range groups {
		path := base + "/" + strconv.FormatInt(group.ID, 10)
		renameQuery := cloneGroupQuery(query)
		renameQuery.Set("rename", "1")
		filterQuery := cloneGroupQuery(query)
		filterQuery.Set("group", encodeGroup(&group.Name))
		view := groupRecordView{Group: group, URL: path + suffix, RenameURL: path + "?" + renameQuery.Encode(), FilterURL: taskURL(data.Project.Code, filterQuery, "", storage.WebPelletSort{})}
		for _, pellet := range pellets {
			if pellet.GroupID != nil && *pellet.GroupID == group.ID {
				view.Members = append(view.Members, makePelletViews([]storage.Pellet{pellet}, data.Project.Code, query, "", storage.WebPelletSort{})[0])
			}
		}
		data.GroupRecords = append(data.GroupRecords, view)
	}
	if len(segments) == 4 {
		if segments[3] == "new" {
			data.NewGroup = true
			return nil
		}
		id, err := parsePositiveID(segments[3])
		if err != nil {
			return domain.NewError(domain.NotFound, "group_not_found", "group does not exist in this project", nil)
		}
		for i := range data.GroupRecords {
			if data.GroupRecords[i].Group.ID == id {
				data.SelectedGroup = &data.GroupRecords[i]
			}
		}
		if data.SelectedGroup == nil {
			return domain.NewError(domain.NotFound, "group_not_found", "group does not exist in this project", nil)
		}
		data.RenameGroup = r.URL.Query().Get("rename") == "1"
		if data.RenameGroup {
			data.CloseURL = data.SelectedGroup.URL
		}
	}
	return nil
}

func (h *handler) mutateGroup(w http.ResponseWriter, r *http.Request, project storage.Project, segments []string) {
	var group storage.Group
	var err error
	path := "/projects/" + url.PathEscape(project.Code) + "/groups/new"
	action := "create"
	allowed := []string{"_csrf", "name"}
	var id, revision int64
	if len(segments) == 5 && (segments[4] == "context" || segments[4] == "rename") {
		action = segments[4]
		id, err = parsePositiveID(segments[3])
		if err != nil {
			h.renderError(w, 404, requestError("group not found"), nil)
			return
		}
		path = "/projects/" + url.PathEscape(project.Code) + "/groups/" + strconv.FormatInt(id, 10)
		allowed = []string{"_csrf", "revision", "context"}
		if action == "rename" {
			allowed[2] = "name"
		}
	} else if len(segments) != 3 {
		http.NotFound(w, r)
		return
	}
	if fieldErr := requireFields(r.PostForm, allowed); fieldErr != nil {
		err = requestError(fieldErr.Error())
	}
	if err == nil && action != "create" {
		revision, err = parsePositiveID(r.PostForm.Get("revision"))
		if err != nil {
			err = requestError("an exact group revision is required")
		}
	}
	if err == nil {
		switch action {
		case "create":
			group, err = h.application.CreateGroupWithContext(r.Context(), project, r.PostForm.Get("name"), "")
		case "rename":
			group, err = h.application.RenameGroup(r.Context(), project, id, revision, r.PostForm.Get("name"))
		case "context":
			group, err = h.application.EditGroupContext(r.Context(), project, id, revision, r.PostForm.Get("context"))
		}
	}
	status := http.StatusOK
	query := r.URL.Query()
	query.Del("rename")
	if err == nil {
		path = "/projects/" + url.PathEscape(project.Code) + "/groups/" + strconv.FormatInt(group.ID, 10)
		if action == "create" {
			status = http.StatusCreated
		}
	} else {
		status = statusForError(err)
		if action == "rename" {
			query.Set("rename", "1")
		}
	}
	refresh := r.Clone(r.Context())
	location := *r.URL
	location.Path, location.RawQuery = path, query.Encode()
	refresh.URL = &location
	data, readErr := h.loadPage(refresh, project.Code, "groups", pathSegments(path))
	if readErr != nil {
		h.renderError(w, statusForError(readErr), readErr, submittedDraft(r.PostForm))
		return
	}
	if err != nil {
		problem := domain.PublicError(err)
		data.Error, data.StatusCode = problem.Message, status
		if problem.Code == "group_revision_conflict" {
			data.Error = "This group changed elsewhere"
		}
		data.Conflict = &conflictView{Draft: submittedDraft(r.PostForm)}
		if data.SelectedGroup != nil {
			data.Conflict.CurrentFields = map[string]string{"name": data.SelectedGroup.Group.Name, "context": data.SelectedGroup.Group.Context}
		}
		// Retain editable raw input, expose the current version, and require an
		// explicit reviewed retry. Live refresh cannot replace this conflict draft.
		h.render(w, status, "inspector", data)
		return
	}
	w.Header().Set("Content-Location", location.RequestURI())
	h.render(w, status, "inspector", data)
}

func cloneGroupQuery(query url.Values) url.Values {
	result := url.Values{}
	for key, values := range query {
		result[key] = append([]string(nil), values...)
	}
	return result
}
