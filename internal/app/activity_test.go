package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pellets/internal/codex"
)

func activityFixtureEvent(method, thread, turn string, item map[string]any) codex.Event {
	params, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "item": item})
	return codex.Event{Method: method, Params: params}
}

func TestActivityMessagesAreUpdatesAndTurnCompletionIsSeparate(t *testing.T) {
	for phase, title := range map[string]string{"commentary": "Agent update", "final_answer": "Agent response", "": "Agent update"} {
		event := activityFixtureEvent("item/started", "thread", "turn", map[string]any{"id": "message", "type": "agentMessage", "phase": phase, "text": "Still working"})
		if got := projectCodexActivity(event, "thread", "turn"); len(got) != 0 {
			t.Fatal("unfinished message snapshot was exposed")
		}
		event.Method = "item/completed"
		got := projectCodexActivity(event, "thread", "turn")
		if len(got) != 1 || got[0].Kind != "message" || got[0].Title != title || got[0].Status != "completed" {
			t.Fatalf("message presentation: %+v", got)
		}
	}
	got := projectCodexActivity(codex.Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)}, "thread", "turn")
	if len(got) != 1 || got[0].Kind != "turn" || got[0].Status != "completed" {
		t.Fatalf("missing turn boundary: %+v", got)
	}
}

func TestActivityFailureStatusWithoutExitAndExplicitToolError(t *testing.T) {
	for _, kind := range []string{"commandExecution", "mcpToolCall", "fileRead"} {
		got := projectCodexActivity(activityFixtureEvent("item/completed", "thread", "turn", map[string]any{
			"id": "failed", "type": kind, "status": "failed", "command": "check", "tool": "check",
			"error":  map[string]any{"message": "Connection refused\nAuthorization: Bearer private-value"},
			"result": map[string]any{"value": "NEVER COPY"},
		}), "thread", "turn")
		if len(got) != 1 || got[0].Status != "failed" || got[0].ExitCode != nil || got[0].Output != "" {
			t.Fatalf("failure without exit/output lost: %+v", got)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "private-value") || strings.Contains(string(encoded), "NEVER COPY") {
			t.Fatalf("unselected/unsafe error data: %s", encoded)
		}
		if kind == "mcpToolCall" && !strings.Contains(got[0].Error, "Connection refused") {
			t.Fatalf("missing explicit tool error: %+v", got)
		}
	}
	got := projectCodexActivity(activityFixtureEvent("item/completed", "thread", "turn", map[string]any{
		"id": "declined", "type": "commandExecution", "status": "declined",
	}), "thread", "turn")
	if len(got) != 1 || got[0].Status != "declined" {
		t.Fatalf("declined command lost: %+v", got)
	}
}

func TestActivityRuntimeRetryIsExplicitScopedAndBounded(t *testing.T) {
	for _, tc := range []struct{ flag, status string }{{`,"willRetry":true`, "retrying"}, {`,"willRetry":false`, "not_retrying"}, {"", "failed"}} {
		event := codex.Event{Method: "error", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","error":{"message":"Connection lost; token=private-value","additionalDetails":"NEVER COPY"}` + tc.flag + `}`)}
		got := projectCodexActivity(event, "thread", "turn")
		if len(got) != 1 || got[0].Kind != "error" || got[0].Status != tc.status || !strings.Contains(got[0].Error, "[redacted]") {
			t.Fatalf("retry projection: %+v", got)
		}
		encoded, _ := json.Marshal(got)
		if strings.Contains(string(encoded), "private-value") || strings.Contains(string(encoded), "NEVER COPY") {
			t.Fatalf("unsafe retry projection: %s", encoded)
		}
		if len(projectCodexActivity(event, "other", "turn")) != 0 || len(projectCodexActivity(event, "thread", "other")) != 0 {
			t.Fatal("unrelated error projected")
		}
	}
	item := sanitizeActivityItem(ActivityItem{Error: strings.Repeat("error ", activityMaxFieldBytes)})
	if !item.Truncated || len(item.Error) > activityMaxFieldBytes || activityItemBytes(item) > activityMaxItemBytes {
		t.Fatalf("error field escaped bounds: %d", len(item.Error))
	}
}

func TestActivityRuntimeErrorsKeepObservationOrderAndReplayIdentity(t *testing.T) {
	for _, repeated := range []bool{false, true} {
		t.Run(fmt.Sprintf("identical=%t", repeated), func(t *testing.T) {
			projection := newActivityProjection()
			key := activeExecutionKey{databasePath: "db", runID: 1}
			projection.begin(key)
			execution := WorkspaceExecution{activity: projection, database: Database{Path: key.databasePath}, id: key.runID}
			firstError := codex.Event{Method: "error", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","error":{"message":"Connection lost; token=private-value"},"willRetry":true}`)}
			secondError := codex.Event{Method: "error", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","error":{"message":"Retry exhausted"},"willRetry":false}`)}
			if repeated {
				secondError = firstError
			}
			command := func(method, id string) codex.Event {
				return activityFixtureEvent(method, "thread", "turn", map[string]any{"id": id, "type": "commandExecution", "command": "go test ./..."})
			}
			execution.ProjectActivity(firstError, "thread", "turn")
			execution.ProjectActivity(command("item/started", "first"), "thread", "turn")
			before := projection.snapshot(key, 0)
			for _, scope := range [][2]string{{"other", "turn"}, {"thread", "other"}, {"", "turn"}, {"thread", ""}} {
				execution.ProjectActivity(secondError, scope[0], scope[1])
			}
			execution.ProjectActivity(secondError, "thread", "turn")
			execution.ProjectActivity(command("item/started", "second"), "thread", "turn")
			snapshot := projection.snapshot(key, 0)
			if snapshot.Cursor != 4 || len(snapshot.Items) != 4 {
				t.Fatalf("lost or unrelated events: %+v", snapshot)
			}
			ids := map[string]bool{}
			for i, kind := range []string{"error", "command", "error", "command"} {
				item := snapshot.Items[i]
				if item.Kind != kind || item.Sequence != uint64(i+1) || item.ID == "" || ids[item.ID] {
					t.Fatalf("observation order/identity: %+v", snapshot.Items)
				}
				ids[item.ID] = true
			}
			if !reflect.DeepEqual(snapshot.Items[:2], before.Items) {
				t.Fatal("later error changed earlier activity")
			}
			if snapshot.Items[0].Status != "retrying" || snapshot.Items[0].Error != "Connection lost; [redacted]" {
				t.Fatalf("first error lost status or sanitization: %+v", snapshot.Items[0])
			}
			if repeated {
				if snapshot.Items[2].Error != snapshot.Items[0].Error || snapshot.Items[2].Status != snapshot.Items[0].Status {
					t.Fatal("identical error changed")
				}
			} else if snapshot.Items[2].Status != "not_retrying" || snapshot.Items[2].Error != "Retry exhausted" {
				t.Fatalf("second error lost context: %+v", snapshot.Items[2])
			}
			delta := projection.snapshot(key, before.Cursor)
			if delta.Reset || !reflect.DeepEqual(delta.Items, snapshot.Items[2:]) {
				t.Fatalf("incremental replay lost order/identity: %+v", delta)
			}
			if replay := projection.snapshot(key, 0); !reflect.DeepEqual(replay, snapshot) {
				t.Fatal("snapshot replay changed retained error identities")
			}
			// A real item completion still replaces its original position, even
			// when a later error and command have already been observed.
			execution.ProjectActivity(command("item/completed", "first"), "thread", "turn")
			completed := projection.snapshot(key, 0)
			if len(completed.Items) != 4 || completed.Items[1].Status != "completed" {
				t.Fatalf("command completion failed to replace: %+v", completed)
			}
			for i, item := range completed.Items {
				if item.ID != snapshot.Items[i].ID || i != 1 && !reflect.DeepEqual(item, snapshot.Items[i]) {
					t.Fatal("command completion moved or replaced unrelated activity")
				}
			}
			if update := projection.snapshot(key, snapshot.Cursor); update.Reset || len(update.Items) != 1 || update.Items[0] != completed.Items[1] {
				t.Fatalf("completion delta: %+v", update)
			}
			if caughtUp := projection.snapshot(key, completed.Cursor); caughtUp.Reset || len(caughtUp.Items) != 0 {
				t.Fatal("caught-up replay duplicated events")
			}
		})
	}
}

func TestActivityRepeatedRuntimeErrorsStayBounded(t *testing.T) {
	projection := newActivityProjection()
	key := activeExecutionKey{databasePath: "db", runID: 1}
	projection.begin(key)
	execution := WorkspaceExecution{activity: projection, database: Database{Path: key.databasePath}, id: key.runID}
	event := codex.Event{Method: "error", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","error":{"message":"Repeated failure"},"willRetry":true}`)}
	ids := map[string]bool{}
	for i := 0; i < activityMaxItems+20; i++ {
		execution.ProjectActivity(event, "thread", "turn")
		snapshot := projection.snapshot(key, 0)
		latest := snapshot.Items[len(snapshot.Items)-1]
		if ids[latest.ID] {
			t.Fatal("a separately received error reused an ID")
		}
		ids[latest.ID] = true
	}
	replay := projection.snapshot(key, 1)
	if !replay.Reset || !replay.Truncated || len(replay.Items) != activityMaxItems || projection.bytes > activityMaxTotalBytes || projection.runs[key].bytes > activityMaxRunBytes {
		t.Fatalf("error retention exceeded bounds: %+v", replay)
	}
	for i, item := range replay.Items {
		if item.Sequence != uint64(i+21) {
			t.Fatal("error retention lost chronological order")
		}
	}
	if snapshot := projection.snapshot(key, 0); !reflect.DeepEqual(snapshot.Items, replay.Items) {
		t.Fatal("retention reset changed surviving identities")
	}
}

func TestActivityProjectsOnlyExactReportedItemsAndRedactsCompletePayloads(t *testing.T) {
	event := activityFixtureEvent("item/completed", "thread", "turn", map[string]any{"id": "command", "type": "commandExecution", "command": "go test ./... --token private-flag", "aggregatedOutput": "ok first\nAuthorization: Bearer private-authorization\nAPI_KEY=private-api\nok last\n", "exitCode": 0, "environment": map[string]any{"secret": "NEVER COPY"}})
	got := projectCodexActivity(event, "thread", "turn")
	if len(got) != 1 || got[0].Kind != "command" || got[0].ExitCode == nil || *got[0].ExitCode != 0 || !strings.Contains(got[0].Output, "ok first\n") || !strings.Contains(got[0].Output, "ok last\n") {
		t.Fatalf("projection: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"private-flag", "private-authorization", "private-api", "NEVER COPY"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("leaked %s: %s", secret, encoded)
		}
	}
	for _, scope := range [][2]string{{"other", "turn"}, {"thread", "other"}, {"", ""}} {
		if len(projectCodexActivity(event, scope[0], scope[1])) != 0 {
			t.Fatal("unrelated event projected")
		}
	}
	for _, method := range []string{"item/commandExecution/outputDelta", "item/agentMessage/delta", "account/updated"} {
		event.Method = method
		if len(projectCodexActivity(event, "thread", "turn")) != 0 {
			t.Fatal("partial or unrecognized payload projected")
		}
	}
}

func TestActivityMultiFileDiffsReadOutputAndSensitivePaths(t *testing.T) {
	event := activityFixtureEvent("item/completed", "thread", "turn", map[string]any{"id": "edit", "type": "fileChange", "changes": []any{map[string]any{"path": "main.go", "diff": "@@ -1 +1 @@\n-old\n+new\n"}, map[string]any{"path": ".env.local", "diff": "+UNLABELLED_VALUE=a-value-to-hide"}}})
	got := projectCodexActivity(event, "thread", "turn")
	if len(got) != 2 || got[0].ID == got[1].ID || got[0].Diff == "" || got[1].Diff != "" || !strings.Contains(got[1].Text, "withheld") {
		t.Fatalf("file projection %+v", got)
	}
	event = activityFixtureEvent("item/completed", "thread", "turn", map[string]any{"id": "read", "type": "commandExecution", "command": "cat main.go", "commandActions": []any{map[string]any{"type": "read", "path": "main.go"}}, "aggregatedOutput": "package main\n"})
	got = projectCodexActivity(event, "thread", "turn")
	if len(got) != 1 || got[0].Kind != "file_read" || got[0].Source != "" || got[0].Output != "package main\n" {
		t.Fatalf("read invented source: %+v", got)
	}
}

func TestActivityRedactionPrecedesTruncationAndPreservesSourceLayout(t *testing.T) {
	value := "\x1b[31mpackage main\x1b[0m\n\t// token=" + strings.Repeat("secret", 5000) + "\nfunc main() {}\u202e\n-----BEGIN RSA PRIVATE KEY-----\nprivate-material\n-----END RSA PRIVATE KEY-----\n"
	got, truncated := sanitizeActivityText(value, 1000)
	if truncated || strings.Contains(got, "secret") || strings.Contains(got, "private-material") || strings.Contains(got, "\x1b") || strings.Contains(got, "\u202e") || !strings.Contains(got, "\n\t// [redacted]\nfunc") {
		t.Fatalf("sanitizer: %q", got)
	}
	got, truncated = sanitizeActivityText(strings.Repeat("é", 1000), 101)
	if !truncated || len(got) > 101 || !strings.Contains(got, "truncated") {
		t.Fatalf("bound: %d %q", len(got), got)
	}
}

func TestActivityBoundsReplayIsolationAndSlowSubscribers(t *testing.T) {
	projection := newActivityProjection()
	key := activeExecutionKey{databasePath: "one.db", runID: 1}
	other := activeExecutionKey{databasePath: "two.db", runID: 1}
	projection.begin(key)
	projection.begin(other)
	events, unsubscribe, err := projection.subscribe(key)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	projection.publish(key, ActivityItem{ID: "first", Kind: "message", Text: "first"})
	first := projection.snapshot(key, 0)
	projection.publish(key, ActivityItem{ID: "first", Kind: "message", Text: "changed"})
	update := projection.snapshot(key, first.Cursor)
	if update.Reset || len(update.Items) != 1 || update.Items[0].Text != "changed" || len(projection.snapshot(other, 0).Items) != 0 {
		t.Fatalf("bad isolated upsert %+v", update)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < activityMaxItems+200; i++ {
			projection.publish(key, ActivityItem{ID: fmt.Sprint(i), Kind: "message", Text: strings.Repeat("x", 5000)})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("slow browser blocked execution")
	}
	if len(events) != 1 {
		t.Fatalf("wakeups not coalesced: %d", len(events))
	}
	replay := projection.snapshot(key, update.Cursor)
	if !replay.Reset || !replay.Truncated || len(replay.Items) > activityMaxItems || projection.bytes > activityMaxTotalBytes || projection.runs[key].bytes > activityMaxRunBytes {
		t.Fatalf("bounds/replay: %+v", replay)
	}
	if snapshot := projection.snapshot(key, replay.Cursor); snapshot.Reset || len(snapshot.Items) != 0 {
		t.Fatal("caught-up cursor repeated history")
	}
	for i := 0; i < activityMaxRuns+1; i++ {
		projection.begin(activeExecutionKey{databasePath: "one.db", runID: int64(i + 2)})
	}
	if projection.snapshot(key, 0).Available || len(projection.runs) > activityMaxRuns {
		t.Fatal("run retention unbounded")
	}
	if projection.snapshot(activeExecutionKey{databasePath: "absent", runID: 1}, 0).Available {
		t.Fatal("absent history fabricated")
	}
}

func TestActivityConcurrentReadFanoutAndSubscriberBound(t *testing.T) {
	projection := newActivityProjection()
	key := activeExecutionKey{databasePath: "db", runID: 1}
	projection.begin(key)
	var cleanups []func()
	for i := 0; i < activityMaxSubscribers; i++ {
		_, cleanup, err := projection.subscribe(key)
		if err != nil {
			t.Fatal(err)
		}
		cleanups = append(cleanups, cleanup)
	}
	if _, _, err := projection.subscribe(key); err == nil {
		t.Fatal("unbounded browser subscriptions")
	}
	var wait sync.WaitGroup
	for i := 0; i < 6; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 200; j++ {
				projection.publish(key, ActivityItem{ID: fmt.Sprint(j), Text: "activity"})
				projection.snapshot(key, uint64(j))
			}
		}()
	}
	wait.Wait()
	for _, cleanup := range cleanups {
		cleanup()
		cleanup()
	}
	if projection.clients != 0 || len(projection.subscribers) != 0 {
		t.Fatal("leaked browser subscriptions")
	}
}

func TestActivityDeterministicPeerRetainsActualCommandsAndRunCompletes(t *testing.T) {
	executable := installSupervisorPeer(t)
	scheduler, request, _ := schedulerFixture(t, executable, "schedule_activity")
	status := awaitSchedule(t, startSchedule(t, scheduler, request))
	if status.State != "completed" {
		t.Fatalf("driver did not complete: %+v", status)
	}
	runs, err := scheduler.options.Supervisor.ListWorkspaceRuns(context.Background(), scheduler.options.Database, request.Selected.Workspace.ID, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %+v %v", runs, err)
	}
	snapshot := scheduler.options.Supervisor.SnapshotActivity(scheduler.options.Database, runs[0].ID, 0)
	kinds := map[string]bool{}
	for _, item := range snapshot.Items {
		kinds[item.Kind] = true
	}
	for _, kind := range []string{"command", "file_read", "file_change", "message", "progress"} {
		if !kinds[kind] {
			t.Fatalf("missing reported %s: %+v", kind, snapshot)
		}
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "private-activity-value") || !strings.Contains(string(encoded), "[redacted]") {
		t.Fatal("peer output was not sanitized")
	}
}

func TestActivityCredentialAssignmentsInSourceAndConfig(t *testing.T) {
	for _, value := range []string{`AWS_SECRET_ACCESS_KEY=private-value`, `password := "private-value"`, `clientSecret: "private-value"`, `access_key_id => 'private-value'`, `Cookie: private-value`, `--api-key 'private-value with spaces'`} {
		got, _ := sanitizeActivityText(value, 1000)
		if strings.Contains(got, "private-value") || !strings.Contains(got, "[redacted]") {
			t.Fatalf("credential exposed: %q => %q", value, got)
		}
	}
}

func TestActivityResolvedInteractionUpdatesPendingItemWithoutAnswers(t *testing.T) {
	p := newActivityProjection()
	key := activeExecutionKey{databasePath: "db", runID: 1}
	p.begin(key)
	p.publish(key, ActivityItem{ID: "question", Kind: "question", Title: "Input requested", Status: "awaiting_input"})
	before := p.snapshot(key, 0)
	e := WorkspaceExecution{activity: p, database: Database{Path: "db"}, id: 1}
	e.recordActivityAction(2, "question", "Response delivered", "resolved")
	after := p.snapshot(key, before.Cursor)
	if len(after.Items) != 2 || after.Items[0].Status != "resolved" || after.Items[0].ID != "question" {
		t.Fatalf("pending interaction did not resolve: %+v", after)
	}
}

func TestActivityTotalMemoryBoundAcrossCompletedRuns(t *testing.T) {
	projection := newActivityProjection()
	for runID := int64(1); runID <= 12; runID++ {
		key := activeExecutionKey{databasePath: "db", runID: runID}
		projection.begin(key)
		for i := 0; i < 70; i++ {
			projection.publish(key, ActivityItem{ID: fmt.Sprint(i), Kind: "message", Text: strings.Repeat("x", activityMaxFieldBytes)})
		}
		if projection.bytes > activityMaxTotalBytes {
			t.Fatalf("unbounded total memory: %d", projection.bytes)
		}
	}
	if projection.snapshot(activeExecutionKey{databasePath: "db", runID: 1}, 0).Available {
		t.Fatal("oldest retained run was not evicted at total memory cap")
	}
	if !projection.snapshot(activeExecutionKey{databasePath: "db", runID: 12}, 0).Available {
		t.Fatal("most recently updated run was evicted first")
	}
}
