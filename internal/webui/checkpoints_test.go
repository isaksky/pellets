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

func TestWebCheckpointCreationAndInspectorUseSharedContract(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx := context.Background()
	p := f.projects[0]
	a, err := f.application.CreatePellet(ctx, p, storage.NewPellet{Title: "selected target"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {testCSRF}, "title": {"Review target"}, "status": {"open"}, "description": {""}, "external_id": {""}, "group": {""}, "review_targets": {a.Reference.String()}}
	response := performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusCreated {
		t.Fatalf("checkpoint creation %d: %s", response.Code, response.Body.String())
	}
	cp, err := f.application.Pellet(ctx, p, domain.PelletReference{ProjectCode: p.Code, Number: 2})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Kind != domain.PelletReviewCheckpoint || cp.Checkpoint.Ready || cp.Checkpoint.Targets[0].Reference != a.Reference.String() {
		t.Fatalf("web creation lost selected scope: %+v", cp)
	}
	response = performRequest(f.handler, http.MethodGet, "/projects/project1/tasks/project1-2", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Review checkpoint") || !strings.Contains(response.Body.String(), "target_incomplete") {
		t.Fatalf("checkpoint inspector %d: %s", response.Code, response.Body.String())
	}
	// The shared writer checks readiness even with the current full-row token.
	_, err = f.application.TransitionPellet(ctx, p, cp.Reference, storage.PelletVersion(cp), storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if domain.PublicError(err).Code != "review_checkpoint_not_ready" {
		t.Fatalf("web bypassed readiness: %v", err)
	}
}
