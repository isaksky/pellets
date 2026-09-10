package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestWebCheckpointCreationAndInspectorUseSharedContract(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx := context.Background()
	p := f.projects[0]
	a, err := f.application.CreatePellet(ctx, p, storage.NewPellet{Title: "selected target"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"_csrf": {testCSRF}, "request_id": {"web-checkpoint-single"}, "title": {"Review target"}, "status": {"open"}, "description": {""}, "external_id": {""}, "group": {""}, "review_targets": {a.Reference.String()}, "review_target_versions": {a.Reference.String() + ":" + storage.PelletVersion(a)}}
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Review checkpoint") || !strings.Contains(response.Body.String(), "target_incomplete") || !strings.Contains(response.Body.String(), "Review outcome") || !strings.Contains(response.Body.String(), "Created follow-ups") {
		t.Fatalf("checkpoint inspector %d: %s", response.Code, response.Body.String())
	}
	// The shared writer checks readiness even with the current full-row token.
	_, err = f.application.TransitionPellet(ctx, p, cp.Reference, storage.PelletVersion(cp), storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if domain.PublicError(err).Code != "review_checkpoint_not_ready" {
		t.Fatalf("web bypassed readiness: %v", err)
	}
}

func TestWebCheckpointComposerPreservesExactIntentAndAddReceipt(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx, project := context.Background(), f.projects[0]
	group, external := "review-group", "review-external"
	first, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "Zulu target", Group: &group, ExternalID: &external})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "Alpha target", Group: &group, ExternalID: &external})
	if err != nil {
		t.Fatal(err)
	}

	// The table may be sorted by title, while the server still chooses the
	// selected target with the latest authoritative priority for placement.
	filters := url.Values{"sort": {"title"}, "direction": {"asc"}, "group": {encodeGroup(&group)}, "external_id": {external}}
	page := performRequest(f.handler, http.MethodGet, "/projects/project1/tasks?"+filters.Encode(), "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("composer page %d: %s", page.Code, page.Body.String())
	}
	for _, required := range []string{
		"data-checkpoint-composer", "data-checkpoint-select", "data-checkpoint-select-all", "Insert review checkpoint",
		`data-filter-external-set="true"`, `data-filter-group-set="true"`, `data-checkpoint-kind="ordinary"`,
	} {
		if !strings.Contains(page.Body.String(), required) {
			t.Fatalf("composer missing %q: %s", required, page.Body.String())
		}
	}

	form := url.Values{
		"_csrf": {testCSRF}, "request_id": {"web-checkpoint-receipt"}, "title": {"Review selected changes"},
		"status": {"open"}, "description": {""}, "external_id": {external}, "group": {group},
		"review_targets":         {first.Reference.String() + "," + second.Reference.String()},
		"review_target_versions": {first.Reference.String() + ":" + storage.PelletVersion(first) + "," + second.Reference.String() + ":" + storage.PelletVersion(second)},
	}
	for attempt := 0; attempt < 2; attempt++ {
		response := performMutation(f.handler, "/projects/project1/pellets?sort=title&direction=asc", form, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code != http.StatusCreated {
			t.Fatalf("checkpoint add attempt %d = %d: %s", attempt, response.Code, response.Body.String())
		}
	}
	checkpoint, err := f.application.Pellet(ctx, project, domain.PelletReference{ProjectCode: project.Code, Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Kind != domain.PelletReviewCheckpoint || checkpoint.Priority == nil || second.Priority == nil || *checkpoint.Priority <= *second.Priority {
		t.Fatalf("checkpoint did not follow the selected priority tail: checkpoint=%+v second=%+v", checkpoint, second)
	}
	rows, err := f.application.Pellets(ctx, project, storage.WebPelletFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("idempotent checkpoint add allocated %d rows, want 3", len(rows))
	}
	// A confirmed create must make the next selected scope use a fresh request
	// ID. The same selection is valid again, but it must allocate a new receipt
	// rather than replaying the prior one.
	form.Set("request_id", "web-checkpoint-next-selection")
	next := performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if next.Code != http.StatusCreated {
		t.Fatalf("next checkpoint with fresh request ID = %d: %s", next.Code, next.Body.String())
	}
	rows, err = f.application.Pellets(ctx, project, storage.WebPelletFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("fresh checkpoint request ID allocated %d rows, want 4", len(rows))
	}
}

func TestWebCheckpointRejectsStaleSelectedTargetVersions(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx, project := context.Background(), f.projects[0]
	target, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "target"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "other"})
	if err != nil {
		t.Fatal(err)
	}

	post := func(version, requestID string) *httptest.ResponseRecorder {
		t.Helper()
		return performMutation(f.handler, "/projects/project1/pellets", url.Values{
			"_csrf": {testCSRF}, "request_id": {requestID}, "title": {"Review target"}, "status": {"open"}, "description": {""}, "external_id": {""}, "group": {""},
			"review_targets": {target.Reference.String()}, "review_target_versions": {target.Reference.String() + ":" + version},
		}, testOrigin, true, "application/x-www-form-urlencoded")
	}
	assertStale := func(version, requestID string) {
		t.Helper()
		response := post(version, requestID)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "changed elsewhere") {
			t.Fatalf("stale selected target = %d: %s", response.Code, response.Body.String())
		}
	}

	stale := storage.PelletVersion(target)
	title := "edited target"
	target, err = f.application.UpdatePellet(ctx, project, target.Reference, stale, storage.PelletChanges{Title: &title})
	if err != nil {
		t.Fatal(err)
	}
	assertStale(stale, "stale-after-edit")

	stale = storage.PelletVersion(target)
	target, err = f.application.MovePellet(ctx, project, target.Reference, stale, storage.PelletPlacement{Target: other.Reference, Before: false})
	if err != nil {
		t.Fatal(err)
	}
	assertStale(stale, "stale-after-reorder")

	stale = storage.PelletVersion(target)
	started, err := f.application.TransitionPellet(ctx, project, target.Reference, stale, storage.PelletLifecycleRequest{Operation: storage.PelletStart})
	if err != nil {
		t.Fatal(err)
	}
	target = started.Pellet
	assertStale(stale, "stale-after-transition")

	rows, err := f.application.Pellets(ctx, project, storage.WebPelletFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("stale selected targets created checkpoint rows: %d", len(rows))
	}
}

func TestWebCheckpointLostResponseReplaySurvivesProjectRename(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx, project := context.Background(), f.projects[0]
	target, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "target"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf": {testCSRF}, "request_id": {"rename-safe-replay"}, "title": {"Review target"}, "status": {"open"}, "description": {""}, "external_id": {""}, "group": {""},
		"review_targets": {target.Reference.String()}, "review_target_versions": {target.Reference.String() + ":" + storage.PelletVersion(target)},
	}
	first := performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if first.Code != http.StatusCreated {
		t.Fatalf("initial checkpoint = %d: %s", first.Code, first.Body.String())
	}

	projects, err := sqlite.OpenProjectDatabase(ctx, f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := projects.PlanProjectRename(ctx, project.ID, "renamed")
	if err == nil {
		_, err = projects.RenameProject(ctx, storage.ProjectRenameRequest{ProjectID: plan.Project.ID, NewCode: plan.NewCode})
	}
	if closeErr := projects.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	// This is the unchanged old-page body sent again after its first response
	// was lost. Both target tuple fields must normalize aliases identically.
	replay := performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if replay.Code != http.StatusCreated || replay.Header().Get("Content-Location") != "/projects/renamed/tasks/renamed-2" {
		t.Fatalf("renamed replay = %d location=%q: %s", replay.Code, replay.Header().Get("Content-Location"), replay.Body.String())
	}
	rows, err := f.application.Pellets(ctx, storage.Project{ID: project.ID, Code: "renamed", Workspaces: project.Workspaces}, storage.WebPelletFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rename replay allocated %d rows, want 2", len(rows))
	}
}
