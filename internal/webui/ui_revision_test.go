package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"pellets/internal/storage"
)

func TestUIRevisionHashesAssetAndTemplateContentsAndPaths(t *testing.T) {
	files := fstest.MapFS{
		"assets/app.js":       {Data: []byte("app")},
		"templates/page.html": {Data: []byte("page")},
	}
	first, err := hashUIRevision(files)
	if err != nil {
		t.Fatal(err)
	}
	again, err := hashUIRevision(files)
	if err != nil || first != again || len(first) != 64 {
		t.Fatalf("unstable revision: %q %q %v", first, again, err)
	}
	for _, path := range []string{"assets/app.js", "templates/page.html"} {
		original := files[path].Data
		files[path].Data = []byte("changed")
		changed, err := hashUIRevision(files)
		if err != nil || changed == first {
			t.Fatalf("revision ignored change to %s: %s %v", path, changed, err)
		}
		files[path].Data = original
	}
	files["assets/renamed.js"] = files["assets/app.js"]
	delete(files, "assets/app.js")
	renamed, err := hashUIRevision(files)
	if err != nil || renamed == first {
		t.Fatalf("revision ignored renamed asset: %s %v", renamed, err)
	}
}

func TestUIRevisionPageAndAssetGraph(t *testing.T) {
	f := newHandlerFixture(t, 1)
	response := performRequest(f.handler, http.MethodGet, "/projects/project1/tasks", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `data-ui-revision="`+uiRevision+`"`) || !strings.Contains(response.Body.String(), `name="pellets-ui-revision" content="`+uiRevision+`"`) {
		t.Fatalf("page revision missing: %d %s", response.Code, response.Body.String())
	}
	for _, asset := range []string{"app.js", "app.css", "workbench.css", "theme-preflight.js"} {
		if !strings.Contains(response.Body.String(), `/assets/`+uiRevision+`/`+asset) {
			t.Fatalf("page asset lacks revision: %s", asset)
		}
	}
	for _, asset := range []string{"app.js", "workbench.js", "dropdowns.js", "filters.js", "datastar-1.0.3.js", "app.css", "workbench.css", "theme-preflight.js"} {
		response := performRequest(f.handler, http.MethodGet, "/assets/"+uiRevision+"/"+asset, "", nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("versioned asset %s: %d %s", asset, response.Code, response.Body.String())
		}
		want, err := embeddedFiles.ReadFile("assets/" + asset)
		if err != nil || response.Body.String() != string(want) {
			t.Fatalf("versioned asset content differs: %s %v", asset, err)
		}
		stale := performRequest(f.handler, http.MethodGet, "/assets/previous-ui/"+asset, "", nil)
		assertUIRevisionChanged(t, stale)
	}
	version := performRequest(f.handler, http.MethodGet, "/ui-version", "", nil)
	if version.Code != http.StatusOK || version.Header().Get(uiRevisionHeader) != uiRevision || version.Header().Get("Cache-Control") != "no-store" || version.Body.String() != "{\"revision\":\""+uiRevision+"\"}\n" {
		t.Fatalf("version endpoint: %d %s", version.Code, version.Body.String())
	}
}

func TestUIRevisionRejectsIncompatibleRequestsBeforeApplicationAccess(t *testing.T) {
	// A nil application ensures stale requests cannot even attempt a read.
	h := &handler{}
	for _, tc := range []struct {
		name, method, path, revision string
		datastar                     bool
	}{
		{"legacy Datastar read", http.MethodGet, "/projects/project1/tasks", "", true},
		{"stale Datastar read", http.MethodGet, "/projects/project1/tasks", "previous-ui", true},
		{"legacy Datastar mutation", http.MethodPost, "/projects/project1/pellets", "", true},
		{"stale JSON read", http.MethodGet, "/projects/project1/runs/1/activity", "previous-ui", false},
		{"stale JSON mutation", http.MethodPost, "/projects/project1/schedules", "previous-ui", false},
		{"stale activity JSON query", http.MethodGet, "/projects/project1/runs/1/activity?ui_revision=previous-ui", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.datastar {
				request.Header.Set("Datastar-Request", "true")
			}
			if tc.revision != "" {
				request.Header.Set(uiRevisionHeader, tc.revision)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			assertUIRevisionChanged(t, response)
		})
	}
}

func TestUIRevisionRejectsStaleMutationWithoutChangingRecord(t *testing.T) {
	f := newHandlerFixture(t, 1)
	pellet, err := f.application.CreatePellet(context.Background(), f.projects[0], storage.NewPellet{Title: "preserved"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "title": {"stale edit"}, "description": {""}, "external_id": {""}, "group": {""}}
	request := httptest.NewRequest(http.MethodPost, testOrigin+"/projects/project1/pellets/"+pellet.Reference.String()+"/edit", strings.NewReader(form.Encode()))
	request.Host = testHost
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set(uiRevisionHeader, "previous-ui")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: testCSRF})
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	assertUIRevisionChanged(t, response)
	unchanged, err := f.application.Pellet(context.Background(), f.projects[0], pellet.Reference)
	if err != nil || storage.PelletVersion(unchanged) != storage.PelletVersion(pellet) {
		t.Fatalf("stale client changed pellet: %+v %v", unchanged, err)
	}
}

func TestUIRevisionStaleStreamsOnlyAnnounceRevision(t *testing.T) {
	for _, path := range []string{"/events?ui_revision=previous-ui", "/projects/project1/runs/1/activity?stream=1&ui_revision=previous-ui"} {
		// These paths must not load a project, run, or subscribe to the hub.
		h := &handler{}
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		want := "event: pellets-ui-revision\ndata: {\"revision\":\"" + uiRevision + "\"}\n\n"
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || response.Header().Get("Cache-Control") != "no-store" || response.Body.String() != want {
			t.Fatalf("stale stream %s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

func assertUIRevisionChanged(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var result struct{ Code, Revision string }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusPreconditionFailed || response.Header().Get(uiRevisionHeader) != uiRevision || response.Header().Get("Cache-Control") != "no-store" || result.Code != "ui_revision_changed" || result.Revision != uiRevision || strings.Contains(response.Body.String(), "datastar-patch") {
		t.Fatalf("revision conflict: %d %s", response.Code, response.Body.String())
	}
}
