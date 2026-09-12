package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestActivityHTTPDurableProjectIsolationCursorSSEAndRestart(t *testing.T) {
	fixture, root := recoveryHandlerFixture(t)
	if _, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "activity integration"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_activity"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := awaitHTTPSchedule(t, fixture, url.Values{"_csrf": {testCSRF}, "workspace_id": {strconv.FormatInt(fixture.projects[0].Workspaces[0].ID, 10)}, "mode": {"run_one"}})
	if receipt.Completed != 1 || receipt.RunID == 0 {
		t.Fatalf("run failed: %+v", receipt)
	}
	path := "/projects/project1/runs/" + strconv.FormatInt(receipt.RunID, 10) + "/activity"
	read := func(path string) app.ActivitySnapshot {
		t.Helper()
		response := performRequest(fixture.handler, http.MethodGet, path, "", nil)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("activity response %d %s", response.Code, response.Body.String())
		}
		var snapshot app.ActivitySnapshot
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(response.Body.String(), "private-activity-value") {
			t.Fatal("credential in HTTP response")
		}
		return snapshot
	}
	snapshot := read(path)
	if !snapshot.Available || !snapshot.Reset || len(snapshot.Items) < 5 {
		t.Fatalf("missing activity: %+v", snapshot)
	}
	cursor := strconv.FormatUint(snapshot.Cursor, 10)
	if delta := read(path + "?after=" + cursor); delta.Reset || len(delta.Items) != 0 {
		t.Fatalf("duplicate replay: %+v", delta)
	}
	if response := performRequest(fixture.handler, http.MethodGet, path+"?after=invalid", "", nil); response.Code != http.StatusUnprocessableEntity {
		t.Fatal("accepted invalid cursor")
	}
	db, err := sqlite.OpenProjectDatabase(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = db.RegisterProject(context.Background(), storage.ProjectRegistration{Code: "other", GitCommonDir: domain.LocalPath{Value: "other/.git", Relative: true}, WorkspaceRoot: domain.LocalPath{Value: "other", Relative: true}, GitDir: domain.LocalPath{Value: "other/.git", Relative: true}})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if response := performRequest(fixture.handler, http.MethodGet, strings.Replace(path, "project1", "other", 1), "", nil); response.Code != http.StatusNotFound {
		t.Fatalf("cross-project history exposed: %d", response.Code)
	}

	// An independent stream connects at the exact cursor, and cancellation ends it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.Host = testHost; fixture.handler.ServeHTTP(w, r) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path+"?stream=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", cursor)
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
	found := false
	eventType := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		}
		if eventType == "pellets-activity" && strings.HasPrefix(line, "data: ") {
			var resumed app.ActivitySnapshot
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &resumed); err != nil {
				t.Fatal(err)
			}
			if resumed.Cursor != snapshot.Cursor || len(resumed.Items) != 0 || resumed.Reset {
				t.Fatalf("bad SSE replay: %+v", resumed)
			}
			found = true
			break
		}
	}
	cancel()
	response.Body.Close()
	if !found {
		t.Fatal("activity stream omitted initial snapshot")
	}

	// A fresh foreground supervisor has durable evidence but no invented history.
	old := fixture.application.Executions
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.application.Executions = app.NewExecutionSupervisor(context.Background(), app.SupervisorOptions{Recorder: app.ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
		return sqlite.OpenExecutionRunDatabase(ctx, path)
	}}})
	defer fixture.application.Executions.Close()
	afterRestart := read(path + "?after=" + cursor)
	if afterRestart.Available || !afterRestart.Reset || len(afterRestart.Items) != 0 {
		t.Fatalf("activity survived restart: %+v", afterRestart)
	}
	run, err := fixture.application.Executions.ReadRun(context.Background(), fixture.application.Database, receipt.RunID)
	if err != nil || run.State != "completed" || fixture.application.Executions.OwnsRun(fixture.application.Database, run.ID) {
		t.Fatalf("history lookup resumed execution: %+v %v", run, err)
	}
}
