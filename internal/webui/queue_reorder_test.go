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

func TestQueueMoveKeepsBrowsingContextAndReportsConflictsBesideQueue(t *testing.T) {
	f := newHandlerFixture(t, 1)
	project := f.projects[0]
	ctx := context.Background()
	a, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "anchor"})
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "review", ReviewTargets: []domain.PelletReference{a.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/projects/project1/pellets/" + review.Reference.String() + "/move"
	returnTo := "/projects/project1/tasks/" + a.Reference.String() + "?direction=asc&execution=1&sort=priority&workspace=1"
	form := url.Values{"_csrf": {testCSRF}, "mode": {"queue"}, "return_to": {returnTo}, "version": {storage.PelletVersion(review)}, "target": {a.Reference.String()}, "target_version": {storage.PelletVersion(a)}, "direction": {"before"}}
	post := func() string {
		response := performRequest(f.handler, http.MethodPost, path, form.Encode(), http.Header{
			"Datastar-Request": {"true"}, "Pellets-Target": {"task-list"}, "Origin": {testOrigin},
			"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
		})
		assertDatastarResult(t, response, http.StatusOK)
		if got := response.Header().Get("Content-Location"); got != returnTo {
			t.Fatalf("queue move changed browse route: %q", got)
		}
		body := response.Body.String()
		for _, want := range []string{"data: selector #task-list\n", "data: selector #tasks-title\n", `id="task-` + review.Reference.String() + `"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("queue result missing %q", want)
			}
		}
		if strings.Contains(body, "data: selector #inspector-host") || strings.Contains(body, "data: selector #app-content") {
			t.Fatal("queue move replaced the selected inspector")
		}
		return body
	}
	post()
	moved, err := f.application.Pellet(ctx, project, review.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Checkpoint == nil || len(moved.Checkpoint.Targets) != 1 || moved.Checkpoint.Targets[0].Reference != a.Reference.String() {
		t.Fatalf("review scope changed: %+v", moved.Checkpoint)
	}
	form.Set("version", storage.PelletVersion(moved))
	form.Set("target", b.Reference.String())
	b, err = f.application.Pellet(ctx, project, b.Reference)
	if err != nil {
		t.Fatal(err)
	}
	form.Set("target_version", storage.PelletVersion(b))
	newTitle := "anchor changed"
	changed, err := f.application.UpdatePellet(ctx, project, b.Reference, storage.PelletVersion(b), storage.PelletChanges{Title: &newTitle})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(f.handler, http.MethodPost, path, form.Encode(), http.Header{
		"Datastar-Request": {"true"}, "Pellets-Target": {"task-list"}, "Origin": {testOrigin},
		"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
	})
	assertDatastarResult(t, response, http.StatusConflict)
	if strings.Contains(response.Body.String(), "data: selector #inspector-host") || !strings.Contains(response.Body.String(), `"queueError":`) {
		t.Fatalf("conflict did not stay in queue receipt: %s", response.Body.String())
	}
	still, err := f.application.Pellet(ctx, project, review.Reference)
	if err != nil || *still.Priority != *moved.Priority {
		t.Fatalf("stale target changed priority: %+v %v", still, err)
	}
	form.Set("target_version", storage.PelletVersion(changed))
	form.Set("return_to", "/projects/project1/tasks?sort=title&direction=asc")
	response = performRequest(f.handler, http.MethodPost, path, form.Encode(), http.Header{
		"Datastar-Request": {"true"}, "Pellets-Target": {"task-list"}, "Origin": {testOrigin},
		"Content-Type": {"application/x-www-form-urlencoded"}, "Cookie": {csrfCookieName + "=" + testCSRF},
	})
	assertDatastarResult(t, response, http.StatusUnprocessableEntity)
}
