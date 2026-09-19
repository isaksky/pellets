package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDesignSystemDevelopmentGate(t *testing.T) {
	for _, development := range []bool{false, true} {
		t.Run(map[bool]string{false: "release", true: "development"}[development], func(t *testing.T) {
			// A nil application proves the gallery does not depend on project data.
			h, err := newHandler(nil, newEventHub(), handlerConfig{Host: testHost, Origin: testOrigin, Development: development})
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/dev/design-system", "/assets/design-system.js", "/assets/design-system.css", "/assets/" + uiRevision + "/design-system.js", "/assets/" + uiRevision + "/design-system.css"} {
				request := httptest.NewRequest(http.MethodGet, testOrigin+path, nil)
				response := httptest.NewRecorder()
				h.ServeHTTP(response, request)
				want := http.StatusNotFound
				if development {
					want = http.StatusOK
				}
				if response.Code != want {
					t.Fatalf("GET %s = %d, want %d: %s", path, response.Code, want, response.Body.String())
				}
				if development && path == "/dev/design-system" {
					if !strings.Contains(response.Header().Get("Content-Security-Policy"), "form-action 'none'") || response.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("unsafe preview headers: %v", response.Header())
					}
					for _, marker := range []string{`id="buttons"`, `id="inputs"`, `id="navigation"`, `id="feedback"`, `id="dialogs"`, `id="foundations"`, `id="ds-record-pellet"`, `id="ds-record-memory"`, `id="ds-record-checkpoint"`, `id="ds-record-conflict"`, `id="insert-dialog"`, `id="plan-new-dialog"`, `class="plan-draft-dialog"`} {
						if !strings.Contains(response.Body.String(), marker) {
							t.Errorf("missing gallery example %s", marker)
						}
					}
					if strings.Contains(response.Body.String(), `/app.js"`) {
						t.Error("gallery must not load application mutations")
					}
				}
			}
			request := httptest.NewRequest(http.MethodPost, testOrigin+"/dev/design-system", nil)
			request.Header.Set("Origin", testOrigin)
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			want := http.StatusNotFound
			if development {
				want = http.StatusMethodNotAllowed
			}
			if response.Code != want {
				t.Errorf("POST gallery = %d, want %d", response.Code, want)
			}
		})
	}
}

func TestDesignSystemNavigationIsDevelopmentOnly(t *testing.T) {
	for _, projects := range []int{0, 1} {
		fixture := newHandlerFixture(t, projects)
		for _, development := range []bool{false, true} {
			h, err := newHandler(fixture.application, newEventHub(), handlerConfig{Host: testHost, Origin: testOrigin, Development: development})
			if err != nil {
				t.Fatal(err)
			}
			path := "/"
			if projects > 0 {
				path = "/projects/project1/tasks"
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, testOrigin+path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("page status: %d: %s", response.Code, response.Body.String())
			}
			if got := strings.Contains(response.Body.String(), `href="/dev/design-system"`); got != development {
				t.Errorf("navigation visible=%t, development=%t, projects=%d", got, development, projects)
			}
		}
	}
}
