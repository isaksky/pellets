package webui

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestDatastarFragmentsAndFullPageNonce(t *testing.T) {
	fixture := newHandlerFixture(t, 1)
	path := "/projects/project1/tasks"
	previousNonce := ""
	for range 2 {
		response := performRequest(fixture.handler, http.MethodGet, path, "", nil)
		match := regexp.MustCompile(`data-nonce="([^"]+)"`).FindStringSubmatch(response.Body.String())
		if len(match) != 2 {
			t.Fatal("full page has no Datastar CSP nonce")
		}
		nonce := html.UnescapeString(match[1])
		policy := response.Header().Get("Content-Security-Policy")
		if nonce == previousNonce || nonce == testCSRF || !strings.Contains(policy, "'nonce-"+nonce+"'") || strings.Contains(policy, "unsafe-") {
			t.Fatalf("nonce is reused, unrelated to CSP, or CSP was relaxed: %q", policy)
		}
		previousNonce = nonce
	}
	for _, target := range []string{"tasks-area", "task-list", "run-dashboard", "project-record", "inspector-host"} {
		response := performRequest(fixture.handler, http.MethodGet, path+"?datastar=%7B%7D", "", http.Header{
			"Datastar-Request": {"true"}, "Pellets-Target": {target},
		})
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" ||
			!strings.Contains(response.Body.String(), "data: selector #"+target+"\n") || strings.Contains(response.Body.String(), "<!doctype") {
			t.Fatalf("%s fragment = %d %s", target, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "datastar=") {
			t.Fatal("transport signals leaked into a navigable URL")
		}
	}
	response := performRequest(fixture.handler, http.MethodGet, path, "", http.Header{"Datastar-Request": {"true"}, "Pellets-Target": {"body"}})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown target = %d", response.Code)
	}
	response = performRequest(fixture.handler, http.MethodGet, path+"?q="+strings.Repeat("x", 1025), "", http.Header{
		"Datastar-Request": {"true"}, "Pellets-Target": {"task-list"},
	})
	assertDatastarResult(t, response, http.StatusUnprocessableEntity)
	if !strings.Contains(response.Body.String(), "data: selector #task-list\n") {
		t.Fatal("filter error would overwrite an unrelated inspector")
	}
}

func TestDatastarUpgradeDoesNotReuseImmutableApplicationJavaScript(t *testing.T) {
	fixture := newHandlerFixture(t, 1)
	for _, asset := range []string{"app.js?v=datastar-1.0.3", "app.css", "theme-preflight.js", "datastar-1.0.3.js"} {
		response := performRequest(fixture.handler, http.MethodGet, "/assets/"+asset, "", nil)
		want := "no-cache"
		if asset == "datastar-1.0.3.js" {
			want = "public, max-age=31536000, immutable"
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != want {
			t.Fatalf("%s cache policy = %q, status %d", asset, response.Header().Get("Cache-Control"), response.Code)
		}
	}
}

func TestDatastarMutationSuccessConflictValidationAndSecurity(t *testing.T) {
	fixture := newHandlerFixture(t, 1)
	pellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "before"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/projects/project1/pellets/" + pellet.Reference.String() + "/edit?sort=title&direction=desc&q=edited"
	form := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "title": {"edited <safe>"}, "external_id": {""}, "group": {""}, "description": {"first\r\nevent: injected\rdata: injected"}}
	post := func(values url.Values) *httptest.ResponseRecorder {
		return performRequest(fixture.handler, http.MethodPost, path, values.Encode(), http.Header{
			"Datastar-Request": {"true"}, "Pellets-Target": {"inspector-host"}, "Origin": {testOrigin},
			"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
		})
	}
	response := post(form)
	assertDatastarResult(t, response, http.StatusOK)
	for _, want := range []string{"data: selector #project-counts", "data: selector #area-tabs", "data: selector #task-list", "q=edited", "sort=title", "direction=desc"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("mutation bundle missing %q", want)
		}
	}
	if !strings.Contains(response.Body.String(), "edited &lt;safe&gt;") || strings.Contains(response.Body.String(), "\nevent: injected") || strings.Contains(response.Body.String(), "\r") {
		t.Fatalf("HTML/SSE text escaped incorrectly: %s", response.Body.String())
	}
	form.Set("title", "preserved stale draft")
	response = post(form)
	assertDatastarResult(t, response, http.StatusConflict)
	if !strings.Contains(response.Body.String(), "preserved stale draft") || !strings.Contains(response.Body.String(), "This record changed elsewhere") {
		t.Fatalf("conflict lost draft: %s", response.Body.String())
	}
	form.Set("title", "")
	response = post(form)
	assertDatastarResult(t, response, http.StatusUnprocessableEntity)
	form.Set("_csrf", "incorrect")
	response = post(form)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "datastar-patch") {
		t.Fatalf("security failure was converted into a patch: %d %s", response.Code, response.Body.String())
	}
}

func assertDatastarResult(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Datastar response = %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Result struct {
			Status int `json:"status"`
		} `json:"_webResult"`
	}
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: signals ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: signals ")), &result); err != nil {
				t.Fatal(err)
			}
		}
	}
	if result.Result.Status != status {
		t.Fatalf("result = %+v, want status %d", result, status)
	}
}

func TestDatastarLiveResponseBundlesRelatedRegions(t *testing.T) {
	fixture := newHandlerFixture(t, 1)
	pellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "live task"})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(fixture.handler, http.MethodGet, "/projects/project1/tasks/"+pellet.Reference.String(), "", http.Header{
		"Datastar-Request": {"true"}, "Pellets-Target": {"live"},
	})
	body := response.Body.String()
	for _, target := range []string{"project-counts", "area-tabs", "project-record", "project-drawer", "task-list", "inspector-host"} {
		if strings.Count(body, "data: selector #"+target+"\n") != 1 {
			t.Errorf("expected one patch for %s", target)
		}
	}
	if strings.Contains(body, "data: mode replace") || !strings.Contains(body, `1 open · 0 active · 0 memories`) {
		t.Fatalf("live response lacks morphed authoritative counts: %s", body)
	}
	if strings.Count(body, "event: datastar-patch-signals") != 1 {
		t.Fatal("bundle must have one completion result")
	}
}

// SDK failures must stop the bundle, including its success/completion signal.
func TestDatastarStopsBundleOnDeliveryFailure(t *testing.T) {
	for _, failure := range []string{"write", "flush", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			sentinel := errors.New("delivery failed")
			writer := &failingSSEWriter{ResponseRecorder: httptest.NewRecorder(), failure: failure, err: sentinel}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response := &datastarResponse{ResponseWriter: writer, request: httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)}
			response.start()
			if failure == "cancel" {
				cancel()
				sentinel = context.Canceled
			}
			response.patch("#task-list", "outer", "<div id=task-list>updated</div>")
			response.patch("#area-tabs", "outer", "<nav id=area-tabs></nav>")
			response.result(http.StatusOK, "/")
			if !errors.Is(response.err, sentinel) {
				t.Fatalf("delivery error = %v, want %v", response.err, sentinel)
			}
			wantWrites := 1
			if failure == "cancel" {
				wantWrites = 0
			}
			if writer.writes != wantWrites || strings.Contains(writer.Body.String(), "datastar-patch-signals") {
				t.Fatalf("bundle continued after failure: writes=%d body=%s", writer.writes, writer.Body.String())
			}
			if writer.Result().Header.Get("Cache-Control") != "no-store" {
				t.Fatal("SDK initialization lost no-store policy")
			}
		})
	}
}

type failingSSEWriter struct {
	*httptest.ResponseRecorder
	failure         string
	err             error
	writes, flushes int
}

func (w *failingSSEWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.failure == "write" {
		return 0, w.err
	}
	return w.ResponseRecorder.Write(data)
}
func (w *failingSSEWriter) FlushError() error {
	w.flushes++
	if w.failure == "flush" && w.flushes > 1 {
		return w.err
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestDatastarEmptyDatabaseBecomesProjectWithoutReload(t *testing.T) {
	fixture := newHandlerFixture(t, 0)
	response := performRequest(fixture.handler, http.MethodGet, "/", "", nil)
	for _, want := range []string{"No registered projects", `pl add "First issue"`, `data-on:pellets-refresh__document`, `id="app-content"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("empty page missing %q", want)
		}
	}
	refresh := func() *httptest.ResponseRecorder {
		return performRequest(fixture.handler, http.MethodGet, "/?datastar=%7B%7D", "", http.Header{"Datastar-Request": {"true"}, "Pellets-Target": {"live"}})
	}
	response = refresh()
	assertDatastarResult(t, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), "No registered projects") || strings.Contains(response.Body.String(), "<!doctype") {
		t.Fatal("empty refresh is not an empty-state patch")
	}
	// Separate database connections model CLI registration and its first issue.
	db, err := sqlite.OpenProjectDatabase(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	project, _, err := db.RegisterProject(context.Background(), storage.ProjectRegistration{Code: "first", GitCommonDir: domain.LocalPath{Value: "first/.git", Relative: true}, WorkspaceRoot: domain.LocalPath{Value: "first", Relative: true}, GitDir: domain.LocalPath{Value: "first/.git", Relative: true}})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := sqlite.OpenPelletRepository(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	if _, err := repo.CreatePellet(context.Background(), storage.ResolvedProject{Project: project, Workspace: project.Workspaces[0]}, storage.NewPellet{Title: "First issue from CLI"}); err != nil {
		t.Fatal(err)
	}
	response = refresh()
	assertDatastarResult(t, response, http.StatusOK)
	body := response.Body.String()
	for _, want := range []string{"data: selector #app-content", `id="task-list"`, "First issue from CLI", `"url":"/projects/first/tasks?direction=asc\u0026sort=priority"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("bootstrap missing %q: %s", want, body)
		}
	}
	if strings.Count(body, "event: datastar-patch-elements") != 1 || strings.Contains(body, "No registered projects") || response.Header().Get("Location") != "" {
		t.Fatalf("bootstrap is not a single shell morph: %s", body)
	}
}
