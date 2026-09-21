package webui

import (
	"context"
	"net/http"
	"pellets/internal/app"
	"pellets/internal/codex"
	"pellets/internal/storage"
	"strings"
	"testing"
	"time"
)

func installCatalogFixture(t *testing.T, f handlerFixture) {
	t.Helper()
	f.application.ModelCatalog = app.NewModelCatalogService(context.Background(), f.application.Reader.(storage.ModelCatalogReader), f.application.Writer.(storage.ModelCatalogWriter), func(context.Context) ([]codex.ModelInfo, error) {
		return []codex.ModelInfo{{Model: "test", DisplayName: "Test"}}, nil
	}, nil)
	f.application.ModelCatalog.Start()
	t.Cleanup(f.application.ModelCatalog.Close)
}
func TestGlobalCatalogHTTP(t *testing.T) {
	f := newHandlerFixture(t, 2)
	installCatalogFixture(t, f)
	for _, url := range []string{"/models", "/projects/" + f.projects[0].Code + "/planning/models", "/projects/" + f.projects[1].Code + "/planning/models?workspace=999&access_mode=bogus"} {
		started := time.Now()
		response := performRequest(f.handler, http.MethodGet, url, "", nil)
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"models"`) || time.Since(started) > time.Second {
			t.Fatal(url, response.Code, response.Body.String())
		}
	}
	response := performRequest(f.handler, http.MethodPost, "/models/refresh", `{"_csrf":"wrong"}`, http.Header{"Content-Type": {"application/json"}, "Origin": {testOrigin}, "Cookie": {csrfCookieName + "=" + testCSRF}})
	if response.Code != 403 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = performRequest(f.handler, http.MethodPost, "/models/refresh", `{"_csrf":"`+testCSRF+`"}`, http.Header{"Content-Type": {"application/json"}, "Origin": {testOrigin}, "Cookie": {csrfCookieName + "=" + testCSRF}})
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestCatalogReadsDoNotWaitForDiscovery(t *testing.T) {
	f := newHandlerFixture(t, 1)
	started := make(chan struct{})
	f.application.ModelCatalog = app.NewModelCatalogService(context.Background(), f.application.Reader.(storage.ModelCatalogReader), f.application.Writer.(storage.ModelCatalogWriter), func(ctx context.Context) ([]codex.ModelInfo, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	f.application.ModelCatalog.Start()
	defer f.application.ModelCatalog.Close()
	<-started
	startedAt := time.Now()
	response := performRequest(f.handler, http.MethodGet, "/models", "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"refreshing":true`) || time.Since(startedAt) > 100*time.Millisecond {
		t.Fatal(response.Code, response.Body.String(), time.Since(startedAt))
	}
}
