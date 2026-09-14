package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"pellets/internal/codex"
	"pellets/internal/storage"
)

func planningRepository(t *testing.T, f handlerFixture) {
	t.Helper()
	f.application.Database.Root = filepath.Dir(f.databasePath)
	for _, p := range f.projects {
		root := filepath.Join(f.application.Database.Root, p.Workspaces[0].RootPath.Value)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
}

func planningHTTP(t *testing.T, f handlerFixture, project string, input any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	method := http.MethodGet
	if input != nil {
		method = http.MethodPost
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, testOrigin+"/projects/"+project+"/planning", &body)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(uiRevisionHeader, uiRevision)
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: testCSRF})
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func planningChatResponse(t *testing.T, w *httptest.ResponseRecorder) storage.PlanningChat {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Chat storage.PlanningChat `json:"chat"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Chat
}
func TestPlanningHTTPPersistenceIsolationAndExplicitCreation(t *testing.T) {
	f := newHandlerFixture(t, 2)
	p := f.projects[0]
	planningRepository(t, f)
	var calls atomic.Int32
	f.application.PlanningGenerate = func(ctx context.Context, options codex.PlanningOptions) (codex.PlanningReply, error) {
		calls.Add(1)
		if !strings.Contains(options.Prompt, "Clarify search") {
			t.Error("missing user planning context")
		}
		return codex.PlanningReply{Text: "Two focused drafts.", Drafts: []codex.PlanningDraft{{Title: "Search clarity", Description: "Clarify search", Group: "web-ui"}, {Title: "Keyboard search", Description: "Handle keyboard", Acceptance: "Escape dismisses"}}, Model: "peer", Effort: "medium"}, nil
	}
	w := planningHTTP(t, f, p.Code, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"chat":null`) {
		t.Fatal(w.Body.String())
	}
	initial := storage.PlanningState{Composer: "Clarify search", Drafts: []storage.PlanningDraft{}, Messages: []storage.PlanningMessage{}}
	create := planningRequest{CSRF: testCSRF, Action: "new", RequestID: "new-chat", State: initial}
	chat := planningChatResponse(t, planningHTTP(t, f, p.Code, create))
	replay := planningChatResponse(t, planningHTTP(t, f, p.Code, create))
	if replay.ID != chat.ID {
		t.Fatal("new chat duplicated")
	}
	send := planningRequest{CSRF: testCSRF, Action: "send", ChatID: chat.ID, Version: chat.Version, RequestID: "send-one", State: chat.State}
	chat = planningChatResponse(t, planningHTTP(t, f, p.Code, send))
	if len(chat.State.Drafts) != 2 || len(chat.State.Messages) != 2 || chat.State.Composer != "" {
		t.Fatalf("chat=%#v", chat)
	}
	replay = planningChatResponse(t, planningHTTP(t, f, p.Code, send))
	if replay.Version != chat.Version || calls.Load() != 1 {
		t.Fatal("send retried model or duplicated result")
	}
	rows, err := f.application.Reader.ListWebPellets(context.Background(), p, storage.WebPelletFilters{})
	if err != nil || len(rows) != 0 {
		t.Fatal("planning changed queue", rows, err)
	}
	foreign := send
	foreign.Version = chat.Version
	w = planningHTTP(t, f, f.projects[1].Code, foreign)
	if w.Code != 404 {
		t.Fatalf("foreign chat HTTP %d %s", w.Code, w.Body.String())
	}
	makePellets := planningRequest{CSRF: testCSRF, Action: "create", ChatID: chat.ID, Version: chat.Version, DraftIDs: []string{chat.State.Drafts[0].ID}}
	chat = planningChatResponse(t, planningHTTP(t, f, p.Code, makePellets))
	if chat.State.Drafts[0].CreatedNumber == 0 || chat.State.Drafts[1].CreatedNumber != 0 {
		t.Fatal("selection not respected")
	}
	planningChatResponse(t, planningHTTP(t, f, p.Code, makePellets))
	rows, err = f.application.Reader.ListWebPellets(context.Background(), p, storage.WebPelletFilters{})
	if err != nil || len(rows) != 1 || rows[0].Workspace != nil {
		t.Fatal("creation duplicated or claimed", rows, err)
	}
	stale := planningRequest{CSRF: testCSRF, Action: "save", ChatID: chat.ID, Version: send.Version, State: initial}
	w = planningHTTP(t, f, p.Code, stale)
	if w.Code != 409 {
		t.Fatalf("stale save HTTP %d %s", w.Code, w.Body.String())
	}
	stale.CSRF = "wrong"
	w = planningHTTP(t, f, p.Code, stale)
	if w.Code != 403 {
		t.Fatal("missing CSRF protection")
	}
}

func TestPlanningSendRejectsUnrelatedModelEditsAndCancellation(t *testing.T) {
	f := newHandlerFixture(t, 1)
	planningRepository(t, f)
	p := f.projects[0]
	state := storage.PlanningState{Composer: "Improve it", Drafts: []storage.PlanningDraft{{ID: "draft-one", Title: "One"}}}
	chat := planningChatResponse(t, planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "new", RequestID: "new-one", State: state}))
	send := planningRequest{CSRF: testCSRF, Action: "send", ChatID: chat.ID, Version: chat.Version, RequestID: "send-one", State: state}
	f.application.PlanningGenerate = func(context.Context, codex.PlanningOptions) (codex.PlanningReply, error) {
		return codex.PlanningReply{Text: "Changed", Drafts: []codex.PlanningDraft{{ID: "draft-one", Title: "Surprise"}}}, nil
	}
	w := planningHTTP(t, f, p.Code, send)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	f.application.PlanningGenerate = func(context.Context, codex.PlanningOptions) (codex.PlanningReply, error) {
		return codex.PlanningReply{}, context.Canceled
	}
	w = planningHTTP(t, f, p.Code, send)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "planning_stopped") {
		t.Fatal(w.Code, w.Body.String())
	}
	saved := planningChatResponse(t, planningHTTP(t, f, p.Code, nil))
	if saved.Version != chat.Version || saved.State.Composer != "Improve it" {
		t.Fatal("failed model changed chat")
	}
}

func TestPlanningInFlightDoesNotLockQueueOrLoseOtherTabEdits(t *testing.T) {
	f := newHandlerFixture(t, 1)
	planningRepository(t, f)
	p := f.projects[0]
	state := storage.PlanningState{Composer: "Plan one issue"}
	chat := planningChatResponse(t, planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "new", RequestID: "new-race", State: state}))
	entered, finish := make(chan struct{}), make(chan struct{})
	f.application.PlanningGenerate = func(ctx context.Context, _ codex.PlanningOptions) (codex.PlanningReply, error) {
		close(entered)
		select {
		case <-finish:
			return codex.PlanningReply{Text: "A proposal", Drafts: []codex.PlanningDraft{{Title: "Proposal", Description: "Details"}}}, nil
		case <-ctx.Done():
			return codex.PlanningReply{}, ctx.Err()
		}
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "send", ChatID: chat.ID, Version: chat.Version, RequestID: "send-race", State: state})
	}()
	<-entered
	blocked := planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "save", ChatID: chat.ID, Version: chat.Version, State: state})
	if blocked.Code != 409 || !strings.Contains(blocked.Body.String(), "planning_busy") {
		close(finish)
		t.Fatal(blocked.Code, blocked.Body.String())
	}
	// Queue writes use a separate transaction while the model waits.
	if _, err := f.application.CreatePellet(context.Background(), p, storage.NewPellet{Title: "Concurrent queue write"}); err != nil {
		close(finish)
		t.Fatal(err)
	}
	// Another application process can update the chat; final model CAS must not
	// overwrite it even though this handler's local busy guard cannot see it.
	changed := state
	changed.Composer = "Another process's draft"
	if _, err := f.application.Writer.(storage.PlanningWriter).SavePlanningChat(context.Background(), p, chat.ID, chat.Version, changed); err != nil {
		close(finish)
		t.Fatal(err)
	}
	close(finish)
	result := <-response
	if result.Code != 409 {
		t.Fatal(result.Code, result.Body.String())
	}
	saved := planningChatResponse(t, planningHTTP(t, f, p.Code, nil))
	if saved.State.Composer != changed.Composer || len(saved.State.Drafts) != 0 {
		t.Fatal("model overwrote concurrent draft")
	}
}

func TestPlanningHTTPRejectsFabricatedHistory(t *testing.T) {
	f := newHandlerFixture(t, 1)
	p := f.projects[0]
	fabricated := storage.PlanningState{Messages: []storage.PlanningMessage{{ID: "fake", Role: "assistant", Text: "I did work"}}}
	w := planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "new", RequestID: "fake-chat", State: fabricated})
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	chat := planningChatResponse(t, planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "new", RequestID: "real-chat", State: storage.PlanningState{}}))
	w = planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "save", ChatID: chat.ID, Version: chat.Version, State: fabricated})
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved := planningChatResponse(t, planningHTTP(t, f, p.Code, nil))
	if len(saved.State.Messages) != 0 {
		t.Fatal("forged transcript saved")
	}
}

func TestPlanningMessageRetryIDsAreExact(t *testing.T) {
	f := newHandlerFixture(t, 1)
	planningRepository(t, f)
	p := f.projects[0]
	var calls int
	f.application.PlanningGenerate = func(context.Context, codex.PlanningOptions) (codex.PlanningReply, error) {
		calls++
		return codex.PlanningReply{Text: "Answer", Drafts: []codex.PlanningDraft{}}, nil
	}
	state := storage.PlanningState{Composer: "First question?"}
	chat := planningChatResponse(t, planningHTTP(t, f, p.Code, planningRequest{CSRF: testCSRF, Action: "new", RequestID: "new-exact", State: state}))
	first := planningRequest{CSRF: testCSRF, Action: "send", ChatID: chat.ID, Version: chat.Version, RequestID: "a:b", State: chat.State}
	chat = planningChatResponse(t, planningHTTP(t, f, p.Code, first))
	state = chat.State
	state.Composer = "Second question?"
	next := planningRequest{CSRF: testCSRF, Action: "send", ChatID: chat.ID, Version: chat.Version, RequestID: "a", State: state}
	planningChatResponse(t, planningHTTP(t, f, p.Code, next))
	if calls != 2 {
		t.Fatal("distinct valid request IDs collided")
	}
	next.State.Composer = "Different request under same ID"
	w := planningHTTP(t, f, p.Code, next)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "planning_request_id_conflict") {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls != 2 {
		t.Fatal("mismatched retry called model")
	}
}
