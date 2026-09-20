package webui

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"pellets/internal/storage"
)

func TestGroupWorkbenchMutations(t *testing.T) {
	f := newHandlerFixture(t, 2)
	ctx := context.Background()
	post := func(path string, values url.Values, status int) string {
		t.Helper()
		values.Set("_csrf", testCSRF)
		response := performMutation(f.handler, path, values, testOrigin, true, "application/x-www-form-urlencoded")
		if response.Code != status {
			t.Fatalf("%s = %d, want %d: %.1000s", path, response.Code, status, response.Body.String())
		}
		return response.Body.String()
	}
	post("/projects/project1/groups", url.Values{"name": {"Shared <context>"}}, 201)
	groups, err := f.application.ListGroups(ctx, f.projects[0])
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups: %v %v", groups, err)
	}
	group := groups[0]
	base := fmt.Sprintf("/projects/project1/groups/%d", group.ID)
	// Explicit creation must never merge with or overwrite an existing group.
	body := post("/projects/project1/groups", url.Values{"name": {group.Name}}, 409)
	if !strings.Contains(body, `name="name" required value="Shared &lt;context&gt;"`) {
		t.Fatal("creation conflict lost draft")
	}
	markdown := "# Shared\n\n```mermaid\ngraph TD; A-->B\n```\n\n<script>unsafe()</script>\n"
	post(base+"/context", url.Values{"revision": {"1"}, "context": {markdown}}, 200)
	draft := "# Retained draft\n"
	body = post(base+"/context", url.Values{"revision": {"1"}, "context": {draft}}, 409)
	for _, text := range []string{html.EscapeString(markdown), draft, `name="revision" value="2"`, "Changes were not saved", `is-dirty`} {
		if !strings.Contains(body, text) {
			t.Fatalf("conflict missing %q", text)
		}
	}
	current, _ := f.application.ReadGroup(ctx, f.projects[0], group.ID)
	if current.Context != markdown {
		t.Fatal("stale write changed context")
	}
	member, err := f.application.CreatePellet(ctx, f.projects[0], storage.NewPellet{Title: "Member", Group: &group.Name})
	if err != nil {
		t.Fatal(err)
	}
	post(base+"/rename", url.Values{"revision": {"2"}, "name": {"Renamed"}}, 200)
	member, _ = f.application.Pellet(ctx, f.projects[0], member.Reference)
	if member.GroupID == nil || *member.GroupID != group.ID || *member.Group != "Renamed" {
		t.Fatal("rename lost stable membership")
	}
	for _, route := range []string{base, "/projects/project1/groups"} {
		response := performRequest(f.handler, "GET", route, "", nil)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "Renamed") {
			t.Fatalf("group view missing: %d", response.Code)
		}
	}
	foreign := fmt.Sprintf("/projects/project2/groups/%d", group.ID)
	if response := performRequest(f.handler, "GET", foreign, "", nil); response.Code != 404 {
		t.Fatal("foreign group readable")
	}
	post(foreign+"/context", url.Values{"revision": {"3"}, "context": {"foreign"}}, 404)
	// A fully URL-escaped, maximum-sized document must pass transport and storage.
	large := strings.Repeat("%", storage.MaxGroupContextBytes)
	post(base+"/context", url.Values{"revision": {"3"}, "context": {large}}, 200)
	post(base+"/context", url.Values{"revision": {"4"}, "context": {large + "x"}}, 422)
	post(base+"/context", url.Values{"revision": {"4"}, "context": {""}}, 200)
	current, _ = f.application.ReadGroup(ctx, f.projects[0], group.ID)
	if current.Context != "" || current.Revision != 5 {
		t.Fatal("clear did not persist")
	}
	_, err = f.application.UpdatePellet(ctx, f.projects[0], member.Reference, storage.PelletVersion(member), storage.PelletChanges{Group: storage.NullableTextChange{Set: true}})
	if err != nil {
		t.Fatal(err)
	}
	response := performRequest(f.handler, http.MethodGet, base, "", nil)
	if !strings.Contains(response.Body.String(), "No member pellets") {
		t.Fatal("empty group did not survive last member")
	}
	post(base+"/rename", url.Values{"revision": {"5"}, "name": {"Other"}, "unexpected": {"field"}}, 422)
}
