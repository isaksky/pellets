package webui

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestClosePelletReturnsQueueWithoutInspector(t *testing.T) {
	for _, query := range []string{"", "?status=all&sort=title&direction=desc&workspace=1&execution=1&q=pellet"} {
		t.Run(query, func(t *testing.T) {
			f := newHandlerFixture(t, 1)
			project := f.projects[0]
			pellet, err := f.application.CreatePellet(context.Background(), project, storage.NewPellet{Title: "Close this pellet"})
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "operation": {"close"}, "recover_workspace_id": {""}, "confirm_recovery": {""}}
			response := performRequest(f.handler, http.MethodPost, "/projects/project1/pellets/"+pellet.Reference.String()+"/transition"+query, form.Encode(), http.Header{
				"Datastar-Request": {"true"}, "Pellets-Target": {"inspector-host"}, "Origin": {testOrigin},
				"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
			})
			assertDatastarResult(t, response, http.StatusOK)
			body := response.Body.String()
			if !strings.Contains(body, "data: selector #inspector-host\n") || !strings.Contains(body, "data: selector #task-list\n") || strings.Contains(body, "data-inspector") {
				t.Fatalf("Close must clear the inspector and refresh the queue: %s", body)
			}
			location, err := url.Parse(response.Header().Get("Content-Location"))
			if err != nil || location.Path != "/projects/project1/tasks" {
				t.Fatalf("Close destination = %v, %v", location, err)
			}
			want, _ := url.ParseQuery(strings.TrimPrefix(query, "?"))
			for key := range want {
				if location.Query().Get(key) != want.Get(key) {
					t.Fatalf("Close lost %s: %v", key, location)
				}
			}
			saved, err := f.application.Pellet(context.Background(), project, pellet.Reference)
			if err != nil || saved.Status != domain.PelletClosed {
				t.Fatalf("Close did not save status: %v, %v", saved.Status, err)
			}
			// Replaying a stale lifecycle version still reports a conflict rather
			// than pretending the operation succeeded and dismissing the dialog.
			stale := performMutation(f.handler, "/projects/project1/pellets/"+pellet.Reference.String()+"/transition", form, testOrigin, true, "application/x-www-form-urlencoded")
			if stale.Code != http.StatusConflict {
				t.Fatalf("stale Close = %d", stale.Code)
			}
		})
	}
}
