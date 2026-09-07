package webui

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"pellets/internal/storage"
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
	for _, target := range []string{"tasks-area", "task-list", "workspace-strip", "project-record", "inspector-host"} {
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
	assertDatastarResult(t, response, http.StatusUnprocessableEntity, false)
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
	path := "/projects/project1/pellets/" + pellet.Reference.String() + "/edit"
	form := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "title": {"edited <safe>"}, "external_id": {""}, "group": {""}, "description": {"first\r\nevent: injected\rdata: injected"}}
	post := func(values url.Values) *httptest.ResponseRecorder {
		return performRequest(fixture.handler, http.MethodPost, path, values.Encode(), http.Header{
			"Datastar-Request": {"true"}, "Pellets-Target": {"inspector-host"}, "Origin": {testOrigin},
			"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
		})
	}
	response := post(form)
	assertDatastarResult(t, response, http.StatusOK, true)
	if !strings.Contains(response.Body.String(), "edited &lt;safe&gt;") || strings.Contains(response.Body.String(), "\nevent: injected") || strings.Contains(response.Body.String(), "\r") {
		t.Fatalf("HTML/SSE text escaped incorrectly: %s", response.Body.String())
	}
	form.Set("title", "preserved stale draft")
	response = post(form)
	assertDatastarResult(t, response, http.StatusConflict, false)
	if !strings.Contains(response.Body.String(), "preserved stale draft") || !strings.Contains(response.Body.String(), "This record changed elsewhere") {
		t.Fatalf("conflict lost draft: %s", response.Body.String())
	}
	form.Set("title", "")
	response = post(form)
	assertDatastarResult(t, response, http.StatusUnprocessableEntity, false)
	form.Set("_csrf", "incorrect")
	response = post(form)
	if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "datastar-patch") {
		t.Fatalf("security failure was converted into a patch: %d %s", response.Code, response.Body.String())
	}
}

func assertDatastarResult(t *testing.T, response *httptest.ResponseRecorder, status int, refresh bool) {
	t.Helper()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Datastar response = %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Result struct {
			Status  int  `json:"status"`
			Refresh bool `json:"refresh"`
		} `json:"_webResult"`
	}
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if strings.HasPrefix(line, "data: signals ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: signals ")), &result); err != nil {
				t.Fatal(err)
			}
		}
	}
	if result.Result.Status != status || result.Result.Refresh != refresh {
		t.Fatalf("result = %+v, want status %d refresh %t", result, status, refresh)
	}
}
