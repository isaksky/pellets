package webui

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

const testHost = "127.0.0.1:8181"
const testOrigin = "http://127.0.0.1:8181"
const testCSRF = "4DxEm_aMub4sZQ4Vq1w5BfFe2Cj52whnNWDiVwWIiE0"

type handlerFixture struct {
	application  *app.WebApplication
	handler      http.Handler
	projects     []storage.Project
	databasePath string
}

func newHandlerFixture(t *testing.T, projectCount int) handlerFixture {
	t.Helper()
	databasePath := t.TempDir() + "/pellets.db"
	projectsDB, err := sqlite.OpenProjectDatabase(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	projects := make([]storage.Project, 0, projectCount)
	for index := range projectCount {
		code := fmt.Sprintf("project%d", index+1)
		project, _, err := projectsDB.RegisterProject(context.Background(), storage.ProjectRegistration{
			Code:          code,
			GitCommonDir:  domain.LocalPath{Value: code + "/.git", Relative: true},
			WorkspaceRoot: domain.LocalPath{Value: code, Relative: true},
			GitDir:        domain.LocalPath{Value: code + "/.git", Relative: true},
		})
		if err != nil {
			projectsDB.Close()
			t.Fatal(err)
		}
		projects = append(projects, project)
	}
	if err := projectsDB.Close(); err != nil {
		t.Fatal(err)
	}
	writer, err := sqlite.OpenWebWriter(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sqlite.OpenWebReader(context.Background(), databasePath)
	if err != nil {
		writer.Close()
		t.Fatal(err)
	}
	application := &app.WebApplication{Reader: reader, Writer: writer}
	if len(projects) > 0 {
		application.Current = &storage.ResolvedProject{Project: projects[0], Workspace: projects[0].Workspaces[0]}
	}
	handler, err := newHandler(application, newEventHub(), handlerConfig{Host: testHost, Origin: testOrigin, CSRF: testCSRF})
	if err != nil {
		application.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { application.Close() })
	return handlerFixture{application: application, handler: handler, projects: projects, databasePath: databasePath}
}

func TestHandlerRendersAuthoritativeResponsiveProjectViewsAndEscapesHTML(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	group, externalID := "Group/Exact", "Issue:42"
	created, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{
		Title: `<script>"unsafe"</script> needle`, Description: `<img src=x onerror=alert(1)>`, Group: &group, ExternalID: &externalID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "ungrouped needle"}); err != nil {
		t.Fatal(err)
	}
	memory, err := fixture.application.CreateMemory(context.Background(), fixture.projects[0], storage.NewMemory{Text: `<b>memory</b>`, CreatedBy: domain.MemoryCreatedByAgent})
	if err != nil {
		t.Fatal(err)
	}

	response := performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d; body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, required := range []string{"project1", "workspace-strip", "New task", "All states", "System", "Dark", "Light", "htmx-2.0.4.min.js"} {
		if !strings.Contains(body, required) {
			t.Fatalf("page missing %q", required)
		}
	}
	if strings.Contains(body, "project-rail") {
		t.Fatal("one-project page rendered a project rail")
	}
	if strings.Contains(body, `<script>"unsafe"</script>`) || !strings.Contains(body, `&lt;script&gt;&#34;unsafe&#34;&lt;/script&gt;`) {
		t.Fatalf("task title was not safely escaped: %s", body)
	}
	if csp := response.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP = %q", csp)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != csrfCookieName || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("CSRF cookies = %#v", cookies)
	}

	filters := url.Values{
		"status": {"open"}, "group": {encodeGroup(&group)}, "external_id": {externalID}, "q": {"needle"},
	}
	response = performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks?"+filters.Encode(), "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), created.Reference.String()) || strings.Contains(response.Body.String(), "ungrouped needle") {
		t.Fatalf("combined filter response = %d %s", response.Code, response.Body.String())
	}

	response = performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks/"+created.Reference.String(), "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `&lt;img src=x onerror=alert(1)&gt;`) || !strings.Contains(response.Body.String(), "Task inspector") || !strings.Contains(response.Body.String(), `task-row status-open selected`) {
		t.Fatalf("deep-link response = %d %s", response.Code, response.Body.String())
	}
	response = performRequest(fixture.handler, http.MethodGet, "/projects/project1/memories", "", nil)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "<b>memory</b>") || !strings.Contains(response.Body.String(), "&lt;b&gt;memory&lt;/b&gt;") {
		t.Fatalf("memory response = %d %s", response.Code, response.Body.String())
	}
	response = performRequest(fixture.handler, http.MethodGet, "/projects/project1/memories/"+strconv.FormatInt(memory.ID, 10), "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Memory inspector") || !strings.Contains(response.Body.String(), `memory-card selected`) {
		t.Fatalf("memory deep-link response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerMultiProjectNavigationDoesNotCrossBoundaries(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 2)
	if _, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "only first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.application.CreatePellet(context.Background(), fixture.projects[1], storage.NewPellet{Title: "only second"}); err != nil {
		t.Fatal(err)
	}
	response := performRequest(fixture.handler, http.MethodGet, "/projects/project2/tasks", "", nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "project-rail") || !strings.Contains(body, "project-drawer") || !strings.Contains(body, "only second") || strings.Contains(body, "only first") {
		t.Fatalf("multi-project response = %d %s", response.Code, body)
	}
}

func TestHandlerRedirectsFormerProjectAndPelletDeepLinksToCanonicalURLs(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	pellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "redirected deep link"})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := sqlite.OpenProjectDatabase(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := projects.PlanProjectRename(context.Background(), fixture.projects[0].ID, "renamed")
	if err != nil {
		projects.Close()
		t.Fatal(err)
	}
	if _, err := projects.RenameProject(context.Background(), storage.ProjectRenameRequest{ProjectID: plan.Project.ID, NewCode: plan.NewCode}); err != nil {
		projects.Close()
		t.Fatal(err)
	}
	if err := projects.Close(); err != nil {
		t.Fatal(err)
	}

	response := performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks/"+pellet.Reference.String()+"?q=redirected", "", nil)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/projects/renamed/tasks/renamed-1?q=redirected" {
		t.Fatalf("former deep link response = %d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	response = performRequest(fixture.handler, http.MethodGet, "/projects/renamed/tasks/renamed-1", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "redirected deep link") || !strings.Contains(response.Body.String(), "project1") || !strings.Contains(response.Body.String(), "renamed") {
		t.Fatalf("canonical deep link response = %d %s", response.Code, response.Body.String())
	}

	canonicalProject, err := fixture.application.Project(context.Background(), "project1")
	if err != nil {
		t.Fatal(err)
	}
	canonicalPellet, err := fixture.application.Pellet(context.Background(), canonicalProject.Project, pellet.Reference)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf": {testCSRF}, "version": {storage.PelletVersion(canonicalPellet)},
		"title": {"edited through former link"}, "description": {""}, "external_id": {""}, "group": {""},
	}
	response = performMutation(fixture.handler, "/projects/project1/pellets/project1-1/edit", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || response.Header().Get("HX-Push-Url") != "/projects/renamed/tasks/renamed-1" || !strings.Contains(response.Body.String(), "edited through former link") {
		t.Fatalf("former mutation link response = %d push=%q body=%s", response.Code, response.Header().Get("HX-Push-Url"), response.Body.String())
	}
}

func TestHandlerEmptyNoResultsAndManyOpaqueGroups(t *testing.T) {
	t.Parallel()
	empty := newHandlerFixture(t, 0)
	response := performRequest(empty.handler, http.MethodGet, "/", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "No registered projects") || strings.Contains(response.Body.String(), "project-rail") {
		t.Fatalf("empty database response = %d %s", response.Code, response.Body.String())
	}

	fixture := newHandlerFixture(t, 1)
	for index := range 18 {
		group := fmt.Sprintf("Opaque/%02d", index)
		if _, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "group fixture", Group: &group}); err != nil {
			t.Fatal(err)
		}
	}
	response = performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks", "", nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Opaque/00") || !strings.Contains(body, "Opaque/17") {
		t.Fatalf("many-group response = %d %s", response.Code, body)
	}
	response = performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks?status=closed&q=no-such-result", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "No tasks match") || strings.Contains(response.Body.String(), "group fixture") {
		t.Fatalf("no-results response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerShowsDistinctWorktreeOwnersAndRequiresConfirmedRecovery(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	projectsDB, err := sqlite.OpenProjectDatabase(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	project, _, err := projectsDB.RegisterProject(context.Background(), storage.ProjectRegistration{
		Code:          "project1",
		GitCommonDir:  domain.LocalPath{Value: "project1/.git", Relative: true},
		WorkspaceRoot: domain.LocalPath{Value: "project1-linked", Relative: true},
		GitDir:        domain.LocalPath{Value: "project1/.git/worktrees/linked", Relative: true},
	})
	projectsDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	main := storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}
	linked := storage.ResolvedProject{Project: project, Workspace: project.Workspaces[1]}
	repository, err := sqlite.OpenPelletRepository(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.CreatePellet(context.Background(), main, storage.NewPellet{Title: "main workspace task"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreatePellet(context.Background(), linked, storage.NewPellet{Title: "linked workspace task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TransitionPellet(context.Background(), main, first.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart}); err != nil {
		t.Fatal(err)
	}
	linkedStarted, err := repository.TransitionPellet(context.Background(), linked, second.Reference, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	repository.Close()
	if err != nil {
		t.Fatal(err)
	}

	response := performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks/"+second.Reference.String(), "", nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, first.Reference.String()) || !strings.Contains(body, second.Reference.String()) || !strings.Contains(body, "project1-linked") || !strings.Contains(body, "does not authenticate an agent") {
		t.Fatalf("worktree/recovery response = %d %s", response.Code, body)
	}

	form := url.Values{
		"_csrf": {testCSRF}, "version": {storage.PelletVersion(linkedStarted.Pellet)}, "operation": {"release"},
		"recover_workspace_id": {strconv.FormatInt(linked.Workspace.ID, 10)}, "confirm_recovery": {""},
	}
	path := "/projects/project1/pellets/" + second.Reference.String() + "/transition"
	response = performMutation(fixture.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unconfirmed recovery status = %d", response.Code)
	}
	form.Set("confirm_recovery", "yes")
	response = performMutation(fixture.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Open") {
		t.Fatalf("confirmed recovery response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerDeferAndMemoryEditApprovalUseVersionedDomainPaths(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	pellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "defer from web"})
	if err != nil {
		t.Fatal(err)
	}
	transition := url.Values{
		"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "operation": {"defer"},
		"recover_workspace_id": {""}, "confirm_recovery": {""},
	}
	response := performMutation(fixture.handler, "/projects/project1/pellets/"+pellet.Reference.String()+"/transition", transition, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Maybe later") {
		t.Fatalf("defer response = %d %s", response.Code, response.Body.String())
	}

	memory, err := fixture.application.CreateMemory(context.Background(), fixture.projects[0], storage.NewMemory{Text: "old memory text", CreatedBy: domain.MemoryCreatedByAgent})
	if err != nil {
		t.Fatal(err)
	}
	edit := url.Values{"_csrf": {testCSRF}, "version": {storage.MemoryVersion(memory)}, "text": {"new memory text"}}
	response = performMutation(fixture.handler, "/projects/project1/memories/"+strconv.FormatInt(memory.ID, 10)+"/edit", edit, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "new memory text") || !strings.Contains(response.Body.String(), "Approve current text") {
		t.Fatalf("memory edit response = %d %s", response.Code, response.Body.String())
	}
	current, err := fixture.application.Memory(context.Background(), fixture.projects[0], memory.ID)
	if err != nil {
		t.Fatal(err)
	}
	approve := url.Values{"_csrf": {testCSRF}, "version": {storage.MemoryVersion(current)}}
	response = performMutation(fixture.handler, "/projects/project1/memories/"+strconv.FormatInt(memory.ID, 10)+"/approve", approve, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Human approved") {
		t.Fatalf("memory approval response = %d %s", response.Code, response.Body.String())
	}
}

func TestHandlerMemoryCreationAssignsHumanProvenanceServerSide(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	path := "/projects/project1/memories"

	tampered := url.Values{"_csrf": {testCSRF}, "text": {"client provenance must be rejected"}, "created_by": {"agent"}}
	response := performMutation(fixture.handler, path, tampered, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("client-controlled provenance status = %d; body=%s", response.Code, response.Body.String())
	}

	form := url.Values{"_csrf": {testCSRF}, "text": {"human browser memory"}}
	response = performMutation(fixture.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), "Human approved") {
		t.Fatalf("memory creation response = %d; body=%s", response.Code, response.Body.String())
	}
	memories, err := fixture.application.Memories(context.Background(), fixture.projects[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].CreatedBy != domain.MemoryCreatedByHuman || memories[0].ApprovedAt == nil {
		t.Fatalf("created memories = %#v", memories)
	}
}

func TestHandlerProjectAndWorkspaceNavigationAreLiveFragments(t *testing.T) {
	t.Parallel()
	multiple := newHandlerFixture(t, 2)
	if _, err := multiple.application.CreatePellet(context.Background(), multiple.projects[1], storage.NewPellet{Title: "project two task"}); err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("HX-Request", "true")
	headers.Set("HX-Target", "project-drawer")
	response := performRequest(multiple.handler, http.MethodGet, "/projects/project1/tasks", "", headers)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `id="project-drawer"`) || !strings.Contains(body, `pellets:refresh`) || !strings.Contains(body, "project2") || strings.Contains(body, "<!doctype html>") {
		t.Fatalf("project rail fragment = %d %s", response.Code, body)
	}

	single := newHandlerFixture(t, 1)
	headers.Set("HX-Target", "workspace-strip")
	response = performRequest(single.handler, http.MethodGet, "/projects/project1/tasks", "", headers)
	body = response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `id="workspace-strip"`) || !strings.Contains(body, `pellets:refresh`) || strings.Contains(body, "<!doctype html>") {
		t.Fatalf("workspace strip fragment = %d %s", response.Code, body)
	}
	headers.Set("HX-Target", "project-record")
	response = performRequest(single.handler, http.MethodGet, "/projects/project1/tasks", "", headers)
	body = response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `id="project-record"`) || !strings.Contains(body, "Git common directory") || !strings.Contains(body, "Workspace 1") || strings.Contains(body, "<!doctype html>") {
		t.Fatalf("project record fragment = %d %s", response.Code, body)
	}
}

func TestHandlerMutationSecurityAndOptimisticConflict(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	pellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "before"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/projects/project1/pellets/" + pellet.Reference.String() + "/edit"
	form := url.Values{
		"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "title": {"first edit"},
		"description": {"draft <safe>"}, "external_id": {""}, "group": {""},
	}

	request := httptest.NewRequest(http.MethodPost, testOrigin+path, strings.NewReader(form.Encode()))
	request.Host = "localhost:8181"
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: testCSRF})
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("invalid Host status = %d", response.Code)
	}

	response = performMutation(fixture.handler, path, form, "http://evil.invalid", true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusForbidden {
		t.Fatalf("invalid Origin status = %d", response.Code)
	}
	response = performMutation(fixture.handler, path, form, testOrigin, false, "application/x-www-form-urlencoded")
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF cookie status = %d", response.Code)
	}
	response = performMutation(fixture.handler, path, form, testOrigin, true, "application/json")
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("invalid media type status = %d", response.Code)
	}
	get := performRequest(fixture.handler, http.MethodGet, path, "", nil)
	if get.Code == http.StatusOK {
		t.Fatal("GET mutation route succeeded")
	}
	unchanged, err := fixture.application.Pellet(context.Background(), fixture.projects[0], pellet.Reference)
	if err != nil || unchanged.Title != "before" {
		t.Fatalf("rejected requests mutated pellet: %#v, %v", unchanged, err)
	}

	response = performMutation(fixture.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "first edit") || !strings.Contains(response.Body.String(), "draft &lt;safe&gt;") {
		t.Fatalf("valid edit response = %d %s", response.Code, response.Body.String())
	}
	stale := url.Values{}
	for key, values := range form {
		stale[key] = append([]string(nil), values...)
	}
	stale.Set("title", "preserved stale draft")
	response = performMutation(fixture.handler, path, stale, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "409 conflict") || !strings.Contains(response.Body.String(), "preserved stale draft") || !strings.Contains(response.Body.String(), "first edit") {
		t.Fatalf("stale edit response = %d %s", response.Code, response.Body.String())
	}
	current, err := fixture.application.Pellet(context.Background(), fixture.projects[0], pellet.Reference)
	if err != nil || current.Title != "first edit" {
		t.Fatalf("stale request overwrote row: %#v, %v", current, err)
	}
}

func TestCSRFAndRowCapabilitiesAreNontrivialAndVariable(t *testing.T) {
	t.Parallel()
	first, err := randomCapability()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomCapability()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 40 || len(second) < 40 {
		t.Fatalf("capabilities = %q and %q", first, second)
	}
	if !validVersion(storage.PelletVersion(storage.Pellet{Title: "one"})) || storage.PelletVersion(storage.Pellet{Title: "one"}) == storage.PelletVersion(storage.Pellet{Title: "two"}) {
		t.Fatal("full-row versions are malformed or title-insensitive")
	}
}

func performRequest(handler http.Handler, method, path, body string, headers http.Header) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, testOrigin+path, strings.NewReader(body))
	request.Host = testHost
	for name, values := range headers {
		request.Header[name] = values
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func performMutation(handler http.Handler, path string, form url.Values, origin string, withCookie bool, contentType string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, testOrigin+path, bytes.NewBufferString(form.Encode()))
	request.Host = testHost
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", contentType)
	if withCookie {
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: testCSRF})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
