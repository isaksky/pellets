package webui

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func workbenchFixture(t *testing.T) (handlerFixture, storage.Project, int64, int64) {
	t.Helper()
	f := newHandlerFixture(t, 2)
	for _, path := range []string{"linked", "project1/.git/worktrees/linked"} {
		if err := os.MkdirAll(filepath.Join(filepath.Dir(f.databasePath), path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sqlite.OpenProjectDatabase(context.Background(), f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := db.RegisterProject(context.Background(), storage.ProjectRegistration{Code: "project1", GitCommonDir: domain.LocalPath{Value: "project1/.git", Relative: true}, WorkspaceRoot: domain.LocalPath{Value: "linked", Relative: true}, GitDir: domain.LocalPath{Value: "project1/.git/worktrees/linked", Relative: true}})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	return f, project, project.Workspaces[0].ID, project.Workspaces[1].ID
}
func assignWorkbench(t *testing.T, f handlerFixture, p storage.Project, id int64, mode string, groups []string, ungrouped bool) storage.ProjectRouting {
	t.Helper()
	ctx := context.Background()
	r, err := f.application.Routing(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.application.SaveWorkspaceAssignment(ctx, p, id, r.Version, storage.WorkspaceAssignment{Mode: mode, Groups: groups, IncludeUngrouped: ungrouped})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func addWorkbenchPellet(t *testing.T, f handlerFixture, p storage.Project, title string, group *string, status domain.PelletStatus) storage.Pellet {
	t.Helper()
	pellet, err := f.application.CreatePellet(context.Background(), p, storage.NewPellet{Title: title, Group: group, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return pellet
}

var queueRowPattern = regexp.MustCompile(`<article\b[^>]*data-row-id="(project[0-9]+-[0-9]+)"`)

func workbenchRows(t *testing.T, f handlerFixture, path string) []string {
	t.Helper()
	response := performRequest(f.handler, http.MethodGet, path, "", nil)
	if response.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, response.Code, response.Body.String())
	}
	rows := []string{}
	for _, match := range queueRowPattern.FindAllStringSubmatch(visibleQueueMarkup(response.Body.String()), -1) {
		rows = append(rows, match[1])
	}
	return rows
}
func TestWorkbenchWorkspaceQueuesDefaultActiveRoutingAndOwnership(t *testing.T) {
	f, p, main, linked := workbenchFixture(t)
	web, other := "web-ui", "other"
	a := addWorkbenchPellet(t, f, p, "web open", &web, domain.PelletOpen)
	b := addWorkbenchPellet(t, f, p, "other open", &other, domain.PelletOpen)
	c := addWorkbenchPellet(t, f, p, "ungrouped", nil, domain.PelletOpen)
	d := addWorkbenchPellet(t, f, p, "closed", &web, domain.PelletOpen)
	if _, err := f.application.TransitionPellet(context.Background(), p, d.Reference, storage.PelletVersion(d), storage.PelletLifecycleRequest{Operation: storage.PelletClose}, main); err != nil {
		t.Fatal(err)
	}
	assignWorkbench(t, f, p, linked, "explicit", []string{web}, false)
	base := "/projects/project1/tasks"
	if got := workbenchRows(t, f, base); !slices.Equal(got, []string{a.Reference.String(), b.Reference.String(), c.Reference.String()}) {
		t.Fatalf("default active queue: %v", got)
	}
	if got := workbenchRows(t, f, base+"?workspace="+strconv.FormatInt(main, 10)); !slices.Equal(got, []string{b.Reference.String(), c.Reference.String()}) {
		t.Fatalf("remaining queue: %v", got)
	}
	if got := workbenchRows(t, f, base+"?workspace="+strconv.FormatInt(linked, 10)); !slices.Equal(got, []string{a.Reference.String()}) {
		t.Fatalf("explicit queue: %v", got)
	}
	if got := workbenchRows(t, f, base+"?status=all&workspace="+strconv.FormatInt(linked, 10)); !slices.Equal(got, []string{a.Reference.String(), d.Reference.String()}) {
		t.Fatalf("all lifecycle queue: %v", got)
	}
	owned, err := f.application.TransitionPellet(context.Background(), p, b.Reference, storage.PelletVersion(b), storage.PelletLifecycleRequest{Operation: storage.PelletStart}, main)
	if err != nil {
		t.Fatal(err)
	}
	_ = owned
	assignWorkbench(t, f, p, main, "explicit", []string{web}, false)
	if got := workbenchRows(t, f, base+"?workspace="+strconv.FormatInt(main, 10)); !slices.Equal(got, []string{a.Reference.String(), b.Reference.String(), c.Reference.String()}) {
		t.Fatalf("owned work hidden by reassignment: %v", got)
	}
	// A browsing filter hides rows only; it cannot mutate the routing selection.
	before, _ := f.application.Routing(context.Background(), p)
	_ = workbenchRows(t, f, base+"?workspace="+strconv.FormatInt(main, 10)+"&group="+url.QueryEscape(encodeGroup(&other)))
	after, _ := f.application.Routing(context.Background(), p)
	if before.Version != after.Version {
		t.Fatal("browsing changed routing")
	}
	response := performRequest(f.handler, http.MethodGet, base+"?workspace="+strconv.FormatInt(f.projects[1].Workspaces[0].ID, 10), "", nil)
	if response.Code != 404 {
		t.Fatalf("foreign workspace view: %d", response.Code)
	}
}

func TestWorkbenchClearFiltersPreservesQueueContext(t *testing.T) {
	f, project, main, linked := workbenchFixture(t)
	web, other := "web-ui", "other"
	webPellet := addWorkbenchPellet(t, f, project, "Zulu web", &web, domain.PelletOpen)
	otherPellet := addWorkbenchPellet(t, f, project, "Alpha other", &other, domain.PelletOpen)
	assignWorkbench(t, f, project, linked, "explicit", []string{web}, false)
	clearLink := regexp.MustCompile(`href="([^"]+)"[^>]*>Clear filters</a>`)
	for _, tc := range []struct {
		name      string
		workspace int64
		execution int64
		rows      []string
	}{
		{"workspace queue", linked, linked, []string{webPellet.Reference.String()}},
		{"shared project queue", 0, main, []string{webPellet.Reference.String(), otherPellet.Reference.String()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{"execution": {strconv.FormatInt(tc.execution, 10)}, "sort": {"title"}, "direction": {"desc"}, "q": {"no match"}, "status": {"closed"}, "group": {encodeGroup(&other)}, "external_id": {"exact-filter"}}
			if tc.workspace != 0 {
				query.Set("workspace", strconv.FormatInt(tc.workspace, 10))
			}
			response := performRequest(f.handler, http.MethodGet, "/projects/project1/tasks?"+query.Encode(), "", nil)
			if response.Code != http.StatusOK {
				t.Fatalf("filtered page: %d %s", response.Code, response.Body.String())
			}
			match := clearLink.FindStringSubmatch(response.Body.String())
			if len(match) != 2 {
				t.Fatal("clear filters link missing")
			}
			clearURL, err := url.Parse(html.UnescapeString(match[1]))
			if err != nil {
				t.Fatal(err)
			}
			wantQuery := url.Values{"execution": {strconv.FormatInt(tc.execution, 10)}, "sort": {"title"}, "direction": {"desc"}}
			if tc.workspace != 0 {
				wantQuery.Set("workspace", strconv.FormatInt(tc.workspace, 10))
			}
			if clearURL.Path != "/projects/project1/tasks" || clearURL.Query().Encode() != wantQuery.Encode() {
				t.Fatalf("clear filters destination: %s; want queue context %s", clearURL, wantQuery.Encode())
			}
			if rows := workbenchRows(t, f, clearURL.String()); !slices.Equal(rows, tc.rows) {
				t.Fatalf("cleared queue rows: %v; want %v", rows, tc.rows)
			}
		})
	}
}

func TestWorkbenchCheckpointScopesSurviveFiltersAndUseCurrentTargetRouting(t *testing.T) {
	f, p, main, linked := workbenchFixture(t)
	web, other := "web-ui", "other"
	a := addWorkbenchPellet(t, f, p, "Zulu web target", &web, domain.PelletOpen)
	unrelated := addWorkbenchPellet(t, f, p, "Unrelated intervening", &other, domain.PelletOpen)
	b := addWorkbenchPellet(t, f, p, "Alpha other target", &other, domain.PelletOpen)
	cp, err := f.application.CreatePellet(context.Background(), p, storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "Middle review", ReviewTargets: []domain.PelletReference{a.Reference, b.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	assignWorkbench(t, f, p, linked, "explicit", []string{web}, false)
	base := "/projects/project1/tasks?workspace=" + strconv.FormatInt(linked, 10)
	got := workbenchRows(t, f, base+"&group="+encodeGroup(&web)+"&sort=title&direction=asc")
	if !slices.Equal(got, []string{cp.Reference.String(), a.Reference.String()}) {
		t.Fatalf("filtered scope insertion/sort: %v", got)
	}
	response := performRequest(f.handler, http.MethodGet, base, "", nil)
	if !strings.Contains(response.Body.String(), `data-scope="`+a.Reference.String()+" "+b.Reference.String()+`"`) || !strings.Contains(response.Body.String(), `class="scope-count">2 pellets`) {
		t.Fatal("exact scope count lost")
	}
	if slices.Contains(got, unrelated.Reference.String()) {
		t.Fatal("adjacent unrelated pellet included")
	}
	// Metadata drift updates browsing assignment, but checkpoint snapshot is unchanged.
	_, err = f.application.UpdatePellet(context.Background(), p, a.Reference, storage.PelletVersion(a), storage.PelletChanges{Group: storage.NullableTextChange{Set: true, Value: &other}})
	if err != nil {
		t.Fatal(err)
	}
	if got := workbenchRows(t, f, base); len(got) != 0 {
		t.Fatalf("stale captured group kept checkpoint in former workspace: %v", got)
	}
	if got := workbenchRows(t, f, "/projects/project1/tasks?workspace="+strconv.FormatInt(main, 10)); !slices.Contains(got, cp.Reference.String()) {
		t.Fatalf("checkpoint missing at current target workspace: %v", got)
	}
	preserved, err := f.application.Pellet(context.Background(), p, cp.Reference)
	if err != nil || *preserved.Checkpoint.Targets[0].Group != web || preserved.Checkpoint.Targets[0].Reason != "scope_changed" {
		t.Fatalf("scope evidence changed: %+v %v", preserved, err)
	}
}
func TestWorkbenchAssignmentEndpointsEncodeOpaqueGroupsAndPreserveNavigation(t *testing.T) {
	f, p, _, linked := workbenchFixture(t)
	opaque := " line\r\n\t\"<& group "
	addWorkbenchPellet(t, f, p, "opaque", &opaque, domain.PelletOpen)
	r, _ := f.application.Routing(context.Background(), p)
	page := "/projects/project1/tasks?workspace=" + strconv.FormatInt(linked, 10) + "&q=opaque"
	response := performRequest(f.handler, http.MethodGet, page, "", nil)
	if !strings.Contains(response.Body.String(), `name="groups" value="`+encodeGroup(&opaque)+`"`) {
		t.Fatal("opaque checkbox value not encoded")
	}
	form := url.Values{"_csrf": {testCSRF}, "version": {r.Version}, "mode": {"explicit"}, "groups": {encodeGroup(&opaque)}, "return_to": {page}}
	path := fmt.Sprintf("/projects/project1/workspaces/%d/assignments", linked)
	response = performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != 303 {
		t.Fatalf("assignment save: %d %s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil || location.Query().Get("workspace") != strconv.FormatInt(linked, 10) || location.Query().Get("q") != "opaque" {
		t.Fatalf("save lost workspace/filter: %v %v", location, err)
	}
	saved, err := f.application.Routing(context.Background(), p)
	if err != nil || !slices.Equal(saved.Assignment(linked).Groups, []string{opaque}) {
		t.Fatalf("opaque bytes changed: %+v %v", saved, err)
	}
	form.Set("version", saved.Version)
	form.Set("groups", "n")
	response = performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != 422 {
		t.Fatalf("NULL as named group admitted: %d", response.Code)
	}
	form.Del("groups")
	form.Set("return_to", "https://outside.example/projects/project1/tasks")
	response = performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != 303 || strings.Contains(response.Header().Get("Location"), "outside.example") {
		t.Fatalf("unsafe return route: %d %s", response.Code, response.Header().Get("Location"))
	}
}
func TestWorkbenchRoutingEndpointsConcurrentVersionsAndProjectIsolation(t *testing.T) {
	f, p, _, linked := workbenchFixture(t)
	routing, _ := f.application.Routing(context.Background(), p)
	form := url.Values{"_csrf": {testCSRF}, "version": {routing.Version}, "enabled": {"false"}}
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response := performMutation(f.handler, "/projects/project1/routing", form, testOrigin, true, "application/x-www-form-urlencoded")
			statuses <- response.Code
		}()
	}
	close(start)
	wg.Wait()
	got := []int{<-statuses, <-statuses}
	slices.Sort(got)
	if !slices.Equal(got, []int{303, 409}) {
		t.Fatalf("concurrent updates: %v", got)
	}
	current, _ := f.application.Routing(context.Background(), p)
	foreign, _ := f.application.Routing(context.Background(), f.projects[1])
	if current.Enabled || !foreign.Enabled {
		t.Fatal("project setting leaked")
	}
	form = url.Values{"_csrf": {testCSRF}, "version": {current.Version}, "mode": {"explicit"}, "groups": {encodeGroup(ptrString("web-ui"))}}
	path := fmt.Sprintf("/projects/project1/workspaces/%d/assignments", f.projects[1].Workspaces[0].ID)
	response := performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != 404 {
		t.Fatalf("foreign assignment allowed: %d %s", response.Code, response.Body.String())
	}
	// Authentication envelope is shared with every other mutation.
	response = performMutation(f.handler, fmt.Sprintf("/projects/project1/workspaces/%d/assignments", linked), form, testOrigin, false, "application/x-www-form-urlencoded")
	if response.Code != 403 {
		t.Fatalf("missing CSRF cookie admitted: %d", response.Code)
	}
}
func ptrString(value string) *string { return &value }

func TestWorkbenchRemovedCheckpointsRemainDiscoverableForRestore(t *testing.T) {
	f, p, _, _ := workbenchFixture(t)
	target := addWorkbenchPellet(t, f, p, "target", nil, domain.PelletOpen)
	cp, err := f.application.CreatePellet(context.Background(), p, storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "Review removable", ReviewTargets: []domain.PelletReference{target.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.application.RemoveCheckpoint(context.Background(), p, cp.Reference, storage.PelletVersion(cp))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?status=active"} {
		if got := workbenchRows(t, f, "/projects/project1/tasks"+query); slices.Contains(got, cp.Reference.String()) {
			t.Fatalf("removed checkpoint remained in queue: %v", got)
		}
	}
	if got := workbenchRows(t, f, "/projects/project1/tasks?status=all"); !slices.Contains(got, cp.Reference.String()) {
		t.Fatal("All states must include removed reviews")
	}
	if got := workbenchRows(t, f, "/projects/project1/tasks?status=maybe_later"); !slices.Equal(got, []string{cp.Reference.String()}) {
		t.Fatalf("removed checkpoint cannot be found for restore: %v", got)
	}
	response := performRequest(f.handler, http.MethodGet, "/projects/project1/tasks?status=maybe_later", "", nil)
	if !strings.Contains(response.Body.String(), `class="review-status">Removed</span>`) {
		t.Fatal("removed checkpoint presented as pending review")
	}
	if _, err := f.application.RestoreCheckpoint(context.Background(), p, removed.Reference, storage.PelletVersion(removed)); err != nil {
		t.Fatal(err)
	}
	if got := workbenchRows(t, f, "/projects/project1/tasks"); !slices.Equal(got, []string{target.Reference.String(), cp.Reference.String()}) {
		t.Fatalf("restore did not restore queue position: %v", got)
	}
}

func TestWorkbenchInsertionContextIgnoresDisplayFiltersAndSort(t *testing.T) {
	f, p, _, linked := workbenchFixture(t)
	web, other := "web-ui", "other"
	a := addWorkbenchPellet(t, f, p, "Zulu", &web, domain.PelletOpen)
	b := addWorkbenchPellet(t, f, p, "Alpha", &other, domain.PelletOpen)
	addWorkbenchPellet(t, f, p, "Deferred", nil, domain.PelletMaybeLater)
	cp, err := f.application.CreatePellet(context.Background(), p, storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "Review", ReviewTargets: []domain.PelletReference{a.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	assignWorkbench(t, f, p, linked, "explicit", []string{web}, false)
	request := httptest.NewRequest(http.MethodGet, testOrigin+"/projects/project1/tasks?workspace="+strconv.FormatInt(linked, 10)+"&sort=reference&direction=desc&group="+encodeGroup(&web)+"&q=Zulu", nil)
	h := handler{application: f.application}
	data, err := h.loadPage(request, p.Code, "tasks", []string{"projects", p.Code, "tasks"})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, row := range data.QueueContext {
		got = append(got, row.Pellet.Reference.String())
	}
	if !slices.Equal(got, []string{a.Reference.String(), cp.Reference.String(), b.Reference.String()}) {
		t.Fatalf("insertion context derived from displayed order: %v", got)
	}
	if len(data.ScopeCandidates) != 3 {
		t.Fatalf("scope editor lost filtered/deferred ordinary candidates: %+v", data.ScopeCandidates)
	}
}

func TestWorkbenchShowsEffectiveDefaultsSeparatelyFromSavedChoicesAndFilters(t *testing.T) {
	f, p, main, linked := workbenchFixture(t)
	addWorkbenchPellet(t, f, p, "ungrouped", nil, domain.PelletOpen)
	base := "/projects/project1/tasks?workspace=" + strconv.FormatInt(main, 10) + "&group=n&q=ungrouped"
	response := performRequest(f.handler, http.MethodGet, base, "", nil)
	body := response.Body.String()
	for _, want := range []string{">Receives</span>", "id=\"assignment-remaining\"", "id=\"assignment-ungrouped\"", "· automatic", "Automatic here: all other groups and ungrouped pellets", "Group: Ungrouped", "name=\"include_ungrouped\" value=\"true\" >"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(body, "data-browse-value") {
		t.Fatal("assignment chips must not change browsing filters")
	}
	match := regexp.MustCompile(`href="([^"]+)"[^>]*aria-label="Clear group filter"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing clear group action")
	}
	dest, err := url.Parse(html.UnescapeString(match[1]))
	if err != nil {
		t.Fatal(err)
	}
	if dest.Query().Has("group") || dest.Query().Get("q") != "ungrouped" || dest.Query().Get("workspace") != strconv.FormatInt(main, 10) {
		t.Fatalf("clear group lost other context: %s", dest)
	}
	assignWorkbench(t, f, p, linked, "remaining", nil, true)
	response = performRequest(f.handler, http.MethodGet, base, "", nil)
	if strings.Contains(response.Body.String(), "Automatic here:") {
		t.Fatal("explicit recipient must suppress defaults")
	}
	// A real filter response must update the visible group indicator as well as rows.
	response = performRequest(f.handler, http.MethodGet, base, "", http.Header{"Datastar-Request": {"true"}, "Pellets-Target": {"task-list"}})
	if !strings.Contains(response.Body.String(), "selector #active-group-filter") {
		t.Fatal("group indicator missing from filter update")
	}
}

func TestCategoryRecipientSavePreservesViewAndAllowsAutomaticPlacement(t *testing.T) {
	f, p, main, linked := workbenchFixture(t)
	r, err := f.application.Routing(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	page := "/projects/project1/tasks?workspace=" + strconv.FormatInt(main, 10) + "&group=n"
	form := url.Values{"_csrf": {testCSRF}, "version": {r.Version}, "category": {"ungrouped"}, "recipients": {strconv.FormatInt(linked, 10)}, "return_to": {page}}
	response := performMutation(f.handler, "/projects/project1/routing", form, testOrigin, true, "application/x-www-form-urlencoded")
	destination, _ := url.Parse(response.Header().Get("Location"))
	if response.Code != 303 || destination.Query().Get("workspace") != strconv.FormatInt(main, 10) || destination.Query().Get("group") != "n" {
		t.Fatalf("recipient save lost view: %d %s", response.Code, response.Header().Get("Location"))
	}
	r, err = f.application.Routing(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Selection(linked).AcceptsGroup(nil) || r.Selection(main).AcceptsGroup(nil) {
		t.Fatal("recipient save did not reroute")
	}
	form.Set("version", r.Version)
	form.Del("recipients")
	response = performMutation(f.handler, "/projects/project1/routing", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != 303 {
		t.Fatalf("automatic placement rejected: %d %s", response.Code, response.Body.String())
	}
	r, err = f.application.Routing(context.Background(), p)
	if err != nil || !r.EffectiveAssignment(main).AutomaticUngrouped {
		t.Fatal("missing automatic placement")
	}
}

func TestWorkbenchListsEmptyPersistentGroupsAndKeepsRenamedRouting(t *testing.T) {
	f, p, _, linked := workbenchFixture(t)
	ctx := context.Background()
	name := " empty <Ω> group "
	group, err := f.application.CreateGroup(ctx, p, name)
	if err != nil {
		t.Fatal(err)
	}
	group, err = f.application.EditGroupContext(ctx, p, group.ID, group.Revision, "# Shared Markdown")
	if err != nil {
		t.Fatal(err)
	}
	assignWorkbench(t, f, p, linked, "explicit", []string{name}, false)
	page := "/projects/project1/tasks?workspace=" + strconv.FormatInt(linked, 10)
	response := performRequest(f.handler, http.MethodGet, page, "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `name="groups" value="`+encodeGroup(&name)+`"`) {
		t.Fatalf("empty group missing from assignments: %d", response.Code)
	}
	renamed, err := f.application.RenameGroup(ctx, p, group.ID, group.Revision, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.application.ReadGroup(ctx, p, group.ID)
	if err != nil || current != renamed || current.Context != group.Context {
		t.Fatalf("group read: %+v %v", current, err)
	}
	all, err := f.application.ListGroups(ctx, p)
	if err != nil || len(all) == 0 {
		t.Fatalf("groups: %+v %v", all, err)
	}
	response = performRequest(f.handler, http.MethodGet, page, "", nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), `name="groups" value="`+encodeGroup(&name)+`"`) || !strings.Contains(response.Body.String(), `name="groups" value="`+encodeGroup(&renamed.Name)+`"`) {
		t.Fatal("assignment choices did not refresh after rename")
	}
	routing, err := f.application.Routing(ctx, p)
	if err != nil || !slices.Equal(routing.Assignment(linked).Groups, []string{renamed.Name}) {
		t.Fatalf("renamed routing: %+v %v", routing, err)
	}
}
