package webui

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pellets/internal/app"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

type fakeMonitor struct {
	mu      sync.Mutex
	version int64
	calls   int
	closed  bool
}

func (monitor *fakeMonitor) DataVersion(context.Context) (int64, error) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.calls++
	return monitor.version, nil
}

func (monitor *fakeMonitor) Close() error {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.closed = true
	return nil
}

func (monitor *fakeMonitor) set(version int64) {
	monitor.mu.Lock()
	monitor.version = version
	monitor.mu.Unlock()
}

func (monitor *fakeMonitor) callCount() int {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.calls
}

func TestDataVersionLoopIdlesWithoutClientsCoalescesAndDoesNotBlock(t *testing.T) {
	t.Parallel()
	monitor := &fakeMonitor{version: 1}
	hub := newEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runDataVersionMonitor(ctx, monitor, hub, 4*time.Millisecond, 30*time.Millisecond)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	time.Sleep(18 * time.Millisecond)
	if calls := monitor.callCount(); calls != 0 {
		t.Fatalf("idle monitor calls = %d, want 0", calls)
	}

	first, unsubscribeFirst := hub.subscribe()
	defer unsubscribeFirst()
	second, unsubscribeSecond := hub.subscribe()
	defer unsubscribeSecond()
	waitFor(t, func() bool { return monitor.callCount() >= 1 })
	baselineCalls := monitor.callCount()
	monitor.set(2)
	waitFor(t, func() bool { return monitor.callCount() > baselineCalls })
	monitor.set(3) // joins the pending coalescing window
	afterSecond := monitor.callCount()
	waitFor(t, func() bool { return monitor.callCount() > afterSecond })
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first client did not receive coalesced invalidation")
	}
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("second client did not receive coalesced invalidation")
	}
	select {
	case <-first:
		t.Fatal("burst produced more than one invalidation")
	case <-time.After(25 * time.Millisecond):
	}

	// Leave one client's bounded queue full. Further broadcasts still reach the
	// fast client and return immediately.
	hub.broadcast()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("fast client did not receive direct broadcast")
	}
	started := time.Now()
	hub.broadcast()
	if elapsed := time.Since(started); elapsed > 20*time.Millisecond {
		t.Fatalf("broadcast blocked on slow client for %s", elapsed)
	}
}

func TestSSEContainsOnlyInvalidationAndStopsOnDisconnect(t *testing.T) {
	t.Parallel()
	hub := newEventHub()
	handler, err := newHandler(nil, hub, handlerConfig{Host: testHost, Origin: testOrigin, CSRF: testCSRF})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, testOrigin+"/events", nil).WithContext(ctx)
	request.Host = testHost
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	waitFor(t, func() bool { return hub.clientCount() == 1 })
	hub.broadcast()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after disconnect")
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: pellets-invalidate\ndata: refresh") {
		t.Fatalf("SSE body = %q", body)
	}
	for _, forbidden := range []string{"pellets.db", testCSRF, "project", "row"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("SSE body exposed %q: %q", forbidden, body)
		}
	}
	if hub.clientCount() != 0 {
		t.Fatalf("SSE client leaked; count = %d", hub.clientCount())
	}
}

func TestRealSQLiteMonitorDeliversExternalAndWebCommitsAndMissedEventsRecover(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	monitor, err := sqlite.OpenDataVersionMonitor(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	hub := newEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runDataVersionMonitor(ctx, monitor, hub, 5*time.Millisecond, 8*time.Millisecond)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
		monitor.Close()
	}()
	events, unsubscribe := hub.subscribe()
	time.Sleep(15 * time.Millisecond) // establish the pinned-connection baseline

	webPellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "web writer commit"})
	if err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, "web writer")

	external, err := sqlite.OpenPelletRepository(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	resolved := storage.ResolvedProject{Project: fixture.projects[0], Workspace: fixture.projects[0].Workspaces[0]}
	if _, err := external.CreatePellet(context.Background(), resolved, storage.NewPellet{Title: "external CLI commit"}); err != nil {
		external.Close()
		t.Fatal(err)
	}
	external.Close()
	waitEvent(t, events, "external writer")

	if _, err := fixture.application.Pellet(context.Background(), fixture.projects[0], webPellet.Reference); err != nil {
		t.Fatal(err)
	}
	select {
	case <-events:
		t.Fatal("read-only activity emitted a false invalidation")
	case <-time.After(25 * time.Millisecond):
	}

	raw, err := sql.Open("sqlite", fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE pellets SET title = 'rolled back' WHERE project_id = 1 AND number = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	select {
	case <-events:
		t.Fatal("rollback emitted a false invalidation")
	case <-time.After(25 * time.Millisecond):
	}

	// Disconnect across a commit. No row payload needs replay: a later
	// authoritative GET observes the missed change.
	unsubscribe()
	external, err = sqlite.OpenPelletRepository(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	missed, err := external.CreatePellet(context.Background(), resolved, storage.NewPellet{Title: "missed event recovery"})
	external.Close()
	if err != nil {
		t.Fatal(err)
	}
	listed, err := fixture.application.Pellets(context.Background(), fixture.projects[0], storage.WebPelletFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(listed, func(pellet storage.Pellet) bool { return pellet.Reference == missed.Reference }) {
		t.Fatal("authoritative refresh did not recover a commit missed while disconnected")
	}
}

func TestSlowRenderedGETDoesNotHoldSQLiteReadLock(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	if _, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "initial"}); err != nil {
		t.Fatal(err)
	}
	response := newBlockingResponseWriter()
	request := httptest.NewRequest(http.MethodGet, testOrigin+"/projects/project1/tasks", nil)
	request.Host = testHost
	done := make(chan struct{})
	go func() {
		fixture.handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-response.writing:
	case <-time.After(time.Second):
		t.Fatal("slow response never reached network write")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "while response stalled"})
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("writer during slow response: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow network response retained a SQLite read lock")
	}
	close(response.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("GET did not finish after response unblocked")
	}
}

func TestSlowMutationResponseDoesNotRetainWriterLock(t *testing.T) {
	t.Parallel()
	fixture := newHandlerFixture(t, 1)
	pellet, err := fixture.application.CreatePellet(context.Background(), fixture.projects[0], storage.NewPellet{Title: "before web edit"})
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"_csrf": {testCSRF}, "version": {storage.PelletVersion(pellet)}, "title": {"committed web edit"},
		"description": {""}, "external_id": {""}, "group": {""},
	}
	request := httptest.NewRequest(http.MethodPost, testOrigin+"/projects/project1/pellets/"+pellet.Reference.String()+"/edit", strings.NewReader(form.Encode()))
	request.Host = testHost
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: testCSRF})
	response := newBlockingResponseWriter()
	done := make(chan struct{})
	go func() {
		fixture.handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-response.writing:
	case <-time.After(time.Second):
		t.Fatal("mutation never reached the slow response")
	}

	external, err := sqlite.OpenPelletRepository(context.Background(), fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	resolved := storage.ResolvedProject{Project: fixture.projects[0], Workspace: fixture.projects[0].Workspaces[0]}
	_, writeErr := external.CreatePellet(context.Background(), resolved, storage.NewPellet{Title: "CLI write during slow response"})
	external.Close()
	if writeErr != nil {
		t.Fatalf("writer during slow mutation response: %v", writeErr)
	}
	close(response.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mutation response did not finish after release")
	}
	updated, err := fixture.application.Pellet(context.Background(), fixture.projects[0], pellet.Reference)
	if err != nil || updated.Title != "committed web edit" {
		t.Fatalf("web edit was not committed before response: %#v, %v", updated, err)
	}
}

func TestRunnerBindsLoopbackPrintsReadyURLWarnsOnBrowserFailureAndStops(t *testing.T) {
	fixture := newHandlerFixture(t, 0)
	monitor := &fakeMonitor{}
	var listenedAddress string
	browserCalled := make(chan string, 1)
	output := &lockedBuffer{ready: make(chan struct{})}
	stderr := &lockedBuffer{ready: make(chan struct{})}
	runner := Runner{
		OpenApplication: func(context.Context, string, string, string) (*app.WebApplication, error) {
			return fixture.application, nil
		},
		OpenMonitor: func(context.Context, string) (Monitor, error) { return monitor, nil },
		OpenBrowser: func(target string) error {
			browserCalled <- target
			return errors.New("launcher unavailable")
		},
		Listen: func(network, address string) (net.Listener, error) {
			if network != "tcp4" {
				t.Fatalf("listen network = %q", network)
			}
			listenedAddress = address
			return net.Listen(network, address)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx, Options{DatabasePath: "ignored", NoOpen: false, Stdout: output, Stderr: stderr})
	}()
	select {
	case <-output.ready:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("runner did not print readiness URL")
	}
	if listenedAddress != "127.0.0.1:0" {
		t.Fatalf("listen address = %q, want loopback free port", listenedAddress)
	}
	printed := strings.TrimSpace(output.String())
	select {
	case opened := <-browserCalled:
		if opened != printed {
			t.Fatalf("browser URL = %q, printed %q", opened, printed)
		}
	case <-time.After(time.Second):
		t.Fatal("browser was not launched after readiness")
	}
	if !strings.HasPrefix(printed, "http://127.0.0.1:") {
		t.Fatalf("printed URL = %q", printed)
	}
	select {
	case <-stderr.ready:
	case <-time.After(time.Second):
		t.Fatal("browser failure warning was not written")
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("runner shutdown error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not shut down after cancellation")
	}
	if !monitor.closed {
		t.Fatal("runner did not close monitor")
	}
}

func TestBrowserCommandsArePlatformSpecificAndMacUsesOpen(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		goos string
		name string
		args []string
	}{
		{goos: "darwin", name: "open", args: []string{"http://127.0.0.1:1"}},
		{goos: "windows", name: "rundll32", args: []string{"url.dll,FileProtocolHandler", "http://127.0.0.1:1"}},
		{goos: "linux", name: "xdg-open", args: []string{"http://127.0.0.1:1"}},
	} {
		name, arguments, err := browserCommand(test.goos, "http://127.0.0.1:1")
		if err != nil || name != test.name || strings.Join(arguments, "\x00") != strings.Join(test.args, "\x00") {
			t.Fatalf("browserCommand(%q) = %q %#v, %v", test.goos, name, arguments, err)
		}
	}
	if _, _, err := browserCommand("plan9", "http://127.0.0.1:1"); err == nil {
		t.Fatal("unsupported platform browser command succeeded")
	}
}

type lockedBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (buffer *lockedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	count, err := buffer.data.Write(value)
	buffer.once.Do(func() { close(buffer.ready) })
	return count, err
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.data.String()
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before deadline")
}

func waitEvent(t *testing.T, events <-chan struct{}, source string) {
	t.Helper()
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatalf("no invalidation for %s commit", source)
	}
}

type blockingResponseWriter struct {
	header  http.Header
	writing chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{header: make(http.Header), writing: make(chan struct{}), release: make(chan struct{})}
}

func (writer *blockingResponseWriter) Header() http.Header { return writer.header }
func (writer *blockingResponseWriter) WriteHeader(int)     {}
func (writer *blockingResponseWriter) Write(value []byte) (int, error) {
	writer.once.Do(func() { close(writer.writing) })
	<-writer.release
	return len(value), nil
}
