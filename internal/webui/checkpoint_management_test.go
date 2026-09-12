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

func TestHTTPCheckpointExplicitInsertionUsesExactAnchor(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx, project := context.Background(), f.projects[0]
	a, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "anchor"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "scope after anchor"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf": {testCSRF}, "request_id": {"explicit-before"}, "title": {"Review later target"},
		"description": {""}, "external_id": {""}, "group": {""}, "status": {"open"},
		"review_targets": {b.Reference.String()}, "review_target_versions": {b.Reference.String() + ":" + storage.PelletVersion(b)},
		"placement_target": {a.Reference.String()}, "placement_direction": {"before"}, "placement_target_version": {storage.PelletVersion(a)},
	}
	response := performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusCreated {
		t.Fatalf("explicit insertion: %d %s", response.Code, response.Body.String())
	}
	cp, err := f.application.Pellet(ctx, project, domain.PelletReference{ProjectCode: project.Code, Number: 3})
	a, _ = f.application.Pellet(ctx, project, a.Reference)
	if err != nil || cp.Priority == nil || *cp.Priority >= *a.Priority || cp.Checkpoint.Targets[0].Number != b.Reference.Number {
		t.Fatalf("anchor placement/scope mismatch: %+v %v", cp, err)
	}
	for _, expected := range []string{"Edit scope", "Remove checkpoint", "checkpoint-scope-project1-3", "Explicit review scope", b.Reference.String()} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("checkpoint response missing %q", expected)
		}
	}
	title := "anchor changed"
	if _, err := f.application.UpdatePellet(ctx, project, a.Reference, storage.PelletVersion(a), storage.PelletChanges{Title: &title}); err != nil {
		t.Fatal(err)
	}
	form.Set("request_id", "stale-anchor")
	response = performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "placement_target") || !strings.Contains(response.Body.String(), "changed elsewhere") {
		t.Fatalf("stale insertion did not retain submitted intent: %d %s", response.Code, response.Body.String())
	}
	form.Set("placement_target_version", "")
	response = performMutation(f.handler, "/projects/project1/pellets", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing anchor version accepted: %d", response.Code)
	}
}

func TestHTTPCheckpointScopeRemoveRestoreAndStaleDraft(t *testing.T) {
	f := newHandlerFixture(t, 2)
	ctx, project := context.Background(), f.projects[0]
	a, _ := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "original scope"})
	b, _ := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "new scope"})
	cp, err := f.application.CreatePellet(ctx, project, storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "Review", ReviewTargets: []domain.PelletReference{a.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/projects/project1/checkpoints/" + cp.Reference.String()
	form := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(cp)}, "target": {a.Reference.String() + ":" + storage.PelletVersion(a), b.Reference.String() + ":" + storage.PelletVersion(b)}}
	response := performMutation(f.handler, path+"/scope", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Earlier scope and review history") {
		t.Fatalf("scope save: %d %s", response.Code, response.Body.String())
	}
	changed, _ := f.application.Pellet(ctx, project, cp.Reference)
	if len(changed.Checkpoint.Targets) != 2 || changed.ImplementationRevision != cp.ImplementationRevision+1 {
		t.Fatalf("scope not saved: %+v", changed)
	}
	response = performMutation(f.handler, path+"/scope", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), a.Reference.String()+":"+storage.PelletVersion(a)) || !strings.Contains(response.Body.String(), b.Reference.String()+":"+storage.PelletVersion(b)) {
		t.Fatalf("stale scope draft was not fully preserved: %d %s", response.Code, response.Body.String())
	}
	remove := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(changed)}}
	response = performMutation(f.handler, path+"/remove", remove, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Restore checkpoint") || !strings.Contains(response.Body.String(), "No review was completed") {
		t.Fatalf("reversible removal: %d %s", response.Code, response.Body.String())
	}
	removed, _ := f.application.Pellet(ctx, project, cp.Reference)
	if !removed.Checkpoint.Removed || removed.Status != domain.PelletMaybeLater || removed.CompletedAt != nil {
		t.Fatalf("removal did not defer: %+v", removed)
	}
	remove.Set("version", storage.PelletVersion(removed))
	response = performMutation(f.handler, path+"/restore", remove, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", response.Code, response.Body.String())
	}
	restored, _ := f.application.Pellet(ctx, project, cp.Reference)
	if restored.Checkpoint.Removed || restored.Status != domain.PelletOpen || *restored.Priority <= *a.Priority || *restored.Priority >= *b.Priority {
		t.Fatalf("wrong restored position: %+v", restored)
	}
	remove.Set("version", storage.PelletVersion(restored))
	response = performMutation(f.handler, strings.Replace(path, "/project1/", "/project2/", 1)+"/remove", remove, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-project checkpoint mutation accepted: %d", response.Code)
	}
	response = performMutation(f.handler, path+"/remove", remove, "http://untrusted.invalid", true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusForbidden {
		t.Fatalf("checkpoint mutation bypassed common origin checks: %d", response.Code)
	}
}

func TestHTTPCheckpointScopeRejectsEmptyForgedAndOwnedChanges(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx, project := context.Background(), f.projects[0]
	a, _ := f.application.CreatePellet(ctx, project, storage.NewPellet{Title: "a"})
	cp, _ := f.application.CreatePellet(ctx, project, storage.NewPellet{Kind: domain.PelletReviewCheckpoint, Title: "Review", ReviewTargets: []domain.PelletReference{a.Reference}})
	path := "/projects/project1/checkpoints/" + cp.Reference.String() + "/scope"
	form := url.Values{"_csrf": {testCSRF}, "version": {storage.PelletVersion(cp)}}
	for _, value := range []string{"", a.Reference.String(), cp.Reference.String() + ":" + storage.PelletVersion(cp)} {
		form.Del("target")
		if value != "" {
			form.Set("target", value)
		}
		response := performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid scope accepted: %q => %d %s", value, response.Code, response.Body.String())
		}
	}
	form.Set("target", a.Reference.String()+":"+storage.PelletVersion(a))
	form.Set("operation", "close")
	response := performMutation(f.handler, path, form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("forged lifecycle action accepted: %d", response.Code)
	}
}
