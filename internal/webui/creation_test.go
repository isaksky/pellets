package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestOrdinaryCreationReturnsQueueAndExactReceipt(t *testing.T) {
	for _, query := range []string{"", "?q=unmatched&status=maybe_later&sort=title&direction=desc&workspace=1&execution=1"} {
		t.Run(query, func(t *testing.T) {
			f := newHandlerFixture(t, 1)
			form := url.Values{"_csrf": {testCSRF}, "request_id": {"ordinary-create"}, "title": {"Created once"}, "description": {"Draft"}, "external_id": {""}, "group": {""}, "status": {"open"}}
			post := func() string {
				response := performRequest(f.handler, http.MethodPost, "/projects/project1/pellets"+query, form.Encode(), http.Header{
					"Datastar-Request": {"true"}, "Pellets-Target": {"task-list"}, "Origin": {testOrigin},
					"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
				})
				assertDatastarResult(t, response, http.StatusCreated)
				body := response.Body.String()
				for _, want := range []string{"data: selector #task-list\n", `"createdPellet":"project1-1"`, "data: selector #project-counts"} {
					if !strings.Contains(body, want) {
						t.Fatalf("missing %q: %s", want, body)
					}
				}
				if strings.Contains(body, "data: selector #inspector-host") {
					t.Fatal("ordinary creation opened another editor")
				}
				location, err := url.Parse(response.Header().Get("Content-Location"))
				if err != nil || location.Path != "/projects/project1/tasks" {
					t.Fatalf("creation destination = %v, %v", location, err)
				}
				if query != "" {
					for key, value := range map[string]string{"q": "unmatched", "status": "maybe_later", "sort": "title", "direction": "desc", "workspace": "1", "execution": "1"} {
						if location.Query().Get(key) != value {
							t.Fatalf("lost %s: %v", key, location)
						}
					}
					if strings.Contains(body, `id="task-project1-1"`) {
						t.Fatal("creation silently cleared filters")
					}
				} else if !strings.Contains(body, `id="task-project1-1"`) {
					t.Fatal("created row missing from unfiltered queue")
				}
				return body
			}
			post()
			post() // Replaying a lost receipt must identify the same saved pellet.
		})
	}
}
