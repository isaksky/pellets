package webui

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestWorkspaceDirectoryAndPruning(t *testing.T) {
	f := newHandlerFixture(t, 1)
	ctx := context.Background()
	db, err := sqlite.OpenProjectDatabase(ctx, f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var p storage.Project
	for i := 0; i < 12; i++ {
		p, _, err = db.RegisterProject(ctx, storage.ProjectRegistration{Code: "project1", GitCommonDir: f.projects[0].GitCommonDir, WorkspaceRoot: domain.LocalPath{Value: fmt.Sprintf("agent-%d", i), Relative: true}, GitDir: domain.LocalPath{Value: fmt.Sprintf("project1/.git/worktrees/agent-%d", i), Relative: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	body := performRequest(f.handler, http.MethodGet, "/projects/project1/tasks", "", nil).Body.String()
	nav := body[strings.Index(body, `<nav aria-label="Workspaces">`):]
	nav = nav[:strings.Index(nav, "</nav>")]
	if strings.Contains(nav, "agent-0") || !strings.Contains(nav, "All workspaces (13)") {
		t.Fatalf("unbounded navigation: %s", nav)
	}
	selected := performRequest(f.handler, http.MethodGet, fmt.Sprintf("/projects/project1/tasks?workspace=%d", p.Workspaces[1].ID), "", nil).Body.String()
	if !strings.Contains(selected, `class="workspace-link active"`) {
		t.Fatal("selected workspace hidden")
	}
	for _, headers := range []http.Header{nil, {"Datastar-Request": {"true"}, "Pellets-Target": {"live"}}} {
		response := performRequest(f.handler, http.MethodGet, "/projects/project1/workspaces", "", headers)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "agent-11") || (headers == nil && !strings.Contains(response.Body.String(), "Prune missing workspaces")) {
			t.Fatalf("directory: %d %s", response.Code, response.Body.String())
		}
	}
	form := url.Values{"_csrf": {testCSRF}}
	response := performMutation(f.handler, "/projects/project1/workspaces/prune", form, testOrigin, true, "application/x-www-form-urlencoded")
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "/projects/project1/workspaces") {
		t.Fatalf("prune: %d %s", response.Code, response.Body.String())
	}
	p, err = db.FindProjectByCode(ctx, "project1")
	if err != nil || len(p.Workspaces) != 1 {
		t.Fatalf("prune registration: %+v %v", p, err)
	}
}
