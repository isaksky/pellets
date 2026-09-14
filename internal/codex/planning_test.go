package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func planningTestOptions(t *testing.T, mode string) PlanningOptions {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "fake-mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PELLETS_SUPERVISOR_PEER", "1")
	t.Setenv("PELLETS_CODEX_EXECUTABLE", exe)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	return PlanningOptions{WorkspaceDir: workspace, Prompt: "Please plan input validation and draft preservation."}
}

type planningRecorded struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func planningRecordedCalls(t *testing.T, directory string) []planningRecorded {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "fake-events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []planningRecorded
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var call planningRecorded
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func TestPlanningCatalogUsesRuntimeWithoutStartingModel(t *testing.T) {
	opts := planningTestOptions(t, "planning_chat")
	catalog, err := PlanningModels(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Model != "test-model" || catalog.Effort != "high" || len(catalog.Models) != 2 || !reflect.DeepEqual(catalog.Models[1].SupportedReasoningEfforts, []string{"low", "high"}) {
		t.Fatalf("runtime catalog = %#v", catalog)
	}
	for _, call := range planningRecordedCalls(t, opts.WorkspaceDir) {
		if call.Method == "thread/start" || call.Method == "turn/start" {
			t.Fatalf("catalog contacted model: %s", call.Method)
		}
	}
	opts.Saved.Model, opts.Saved.ReasoningEffort = "retired-model", "unsupported-effort"
	catalog, err = PlanningModels(context.Background(), opts)
	if err != nil || catalog.Model != "test-model" || catalog.Effort != "high" || len(catalog.Models) != 2 {
		t.Fatalf("recoverable catalog = %#v, %v", catalog, err)
	}
	if _, err := Plan(context.Background(), opts); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("planning silently changed stale saved model: %v", err)
	}
}

func TestPlanningUsesIsolatedReadOnlyThreadAndExactCompletedOutput(t *testing.T) {
	opts := planningTestOptions(t, "planning_chat")
	opts.Model, opts.Effort = "test-fast", "low"
	reply, err := Plan(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Drafts) != 2 || reply.Drafts[0].Title != "Validate planning input" || reply.Model != "test-fast" || reply.Effort != "low" {
		t.Fatalf("planning result = %#v", reply)
	}
	threadCount, turnCount := 0, 0
	for _, call := range planningRecordedCalls(t, opts.WorkspaceDir) {
		switch call.Method {
		case "thread/start":
			threadCount++
			var params struct {
				Sandbox, ApprovalPolicy, Model string
				Ephemeral                      bool
				Config                         map[string]json.RawMessage
			}
			if err := json.Unmarshal(call.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Sandbox != "read-only" || params.ApprovalPolicy != "never" || !params.Ephemeral || params.Model != "test-fast" {
				t.Fatalf("thread permissions = %s", call.Params)
			}
			var features map[string]bool
			if json.Unmarshal(params.Config["features"], &features) != nil || len(features) < 10 {
				t.Fatal("tool capabilities missing")
			}
			for name, enabled := range features {
				if enabled {
					t.Fatalf("planning enabled capability %s", name)
				}
			}
			var servers map[string]struct{ Enabled bool }
			if json.Unmarshal(params.Config["mcp_servers"], &servers) != nil || len(servers) != 1 || servers["fixture.external"].Enabled {
				t.Fatalf("external MCP not disabled: %s", params.Config["mcp_servers"])
			}
			if strings.Contains(string(call.Params), "never-copy-me") || strings.Contains(string(call.Params), "never-run-me") {
				t.Fatal("planning copied credential-bearing MCP configuration")
			}
		case "turn/start":
			turnCount++
			var params struct {
				ThreadID, Model, Effort, ApprovalPolicy string
				SandboxPolicy                           struct {
					Type          string
					NetworkAccess bool
				}
				OutputSchema json.RawMessage
			}
			if err := json.Unmarshal(call.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID != "planning-thread" || params.Model != "test-fast" || params.Effort != "low" || params.ApprovalPolicy != "never" || params.SandboxPolicy.Type != "readOnly" || params.SandboxPolicy.NetworkAccess || len(params.OutputSchema) == 0 {
				t.Fatalf("turn permissions/output = %s", call.Params)
			}
		case "thread/resume", "thread/read", "turn/steer", "review/start":
			t.Fatalf("planning invoked execution operation %s", call.Method)
		}
	}
	if threadCount != 1 || turnCount != 1 {
		t.Fatalf("planning threads/turns = %d/%d", threadCount, turnCount)
	}
}

func TestPlanningRefinesExplicitDraftAndAnswersQuestions(t *testing.T) {
	opts := planningTestOptions(t, "planning_chat")
	opts.Prompt = "Context:\n\n" + `{"refine_id":"draft:one","drafts":[{"id":"draft:one","title":"Keep this title","group":"web"}],"messages":[{"text":"Add empty input handling."}]}`
	reply, err := Plan(context.Background(), opts)
	if err != nil || len(reply.Drafts) != 1 || reply.Drafts[0].ID != "draft:one" || reply.Drafts[0].Title != "Keep this title" || reply.Drafts[0].Acceptance == "" {
		t.Fatalf("refinement = %#v, %v", reply, err)
	}
	opts.Prompt = "Context:\n\n" + `{"messages":[{"text":"How do I create this work?"}]}`
	reply, err = Plan(context.Background(), opts)
	if err != nil || len(reply.Drafts) != 0 || reply.Text == "" {
		t.Fatalf("informational answer = %#v, %v", reply, err)
	}
}

func TestPlanningFailuresDoNotReturnProposals(t *testing.T) {
	for _, test := range []struct {
		mode string
		want error
	}{
		{"planning_unsigned", ErrUnauthenticated},
		{"planning_policy_blocked", ErrPlanningUnavailable},
		{"planning_rpc_error", ErrPlanningUnavailable},
		{"planning_interaction", ErrPlanningUnavailable},
		{"planning_failed", ErrPlanningUnavailable},
		{"planning_disconnect", ErrPlanningUnavailable},
		{"planning_bad_json", ErrPlanningOutput},
		{"planning_no_output", ErrPlanningOutput},
		{"planning_oversized", ErrBufferFull},
	} {
		t.Run(test.mode, func(t *testing.T) {
			opts := planningTestOptions(t, test.mode)
			reply, err := Plan(context.Background(), opts)
			if !errors.Is(err, test.want) || len(reply.Drafts) != 0 || reply.Text != "" {
				t.Fatalf("failed response = %#v, %v; want %v", reply, err, test.want)
			}
			if strings.Contains(err.Error(), "API_KEY") || strings.Contains(err.Error(), "secret") {
				t.Fatal("raw RPC details escaped planning error")
			}
		})
	}
}

func TestPlanningCancellationOwnsRuntimeAndReturnsNoResponse(t *testing.T) {
	opts := planningTestOptions(t, "planning_cancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		reply, err := Plan(ctx, opts)
		if reply.Text != "" || len(reply.Drafts) != 0 {
			done <- errors.New("canceled planning returned a response")
			return
		}
		done <- err
	}()
	deadline := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(opts.WorkspaceDir, "fake-planning-started")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("planning exited before start: %v", err)
		case <-deadline:
			t.Fatal("planning never started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("planning runtime did not terminate promptly")
	}
}

func TestPlanningInputAndStructuredOutputBounds(t *testing.T) {
	for _, prompt := range []string{"", strings.Repeat("x", MaxPlanningPromptBytes+1), string([]byte{255})} {
		_, err := Plan(context.Background(), PlanningOptions{Prompt: prompt})
		if !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("invalid prompt accepted: %v", err)
		}
	}
	for _, output := range []string{
		`{"text":"ok","drafts":null}`,
		`{"text":"ok","drafts":[],"unknown":true}`,
		`{"text":"ok","drafts":[]} {}`,
		`{"text":"ok","drafts":[{"title":""}]}`,
		`{"text":"ok","drafts":[{"title":"Missing required fields"}]}`,
		`{"text":"ok","drafts":[{"id":"same","title":"one"},{"id":"same","title":"two"}]}`,
	} {
		if _, err := parsePlanningReply(output); !errors.Is(err, ErrPlanningOutput) {
			t.Fatalf("invalid output accepted: %s (%v)", output, err)
		}
	}
	if _, err := parsePlanningReply(`{"text":"No drafts needed.","drafts":[]}`); err != nil {
		t.Fatal(err)
	}
	opts := planningTestOptions(t, "planning_chat")
	opts.Model = "unadvertised"
	if _, err := Plan(context.Background(), opts); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("unadvertised model = %v", err)
	}
	opts.Model, opts.Effort = "test-model", "ultra"
	if _, err := Plan(context.Background(), opts); !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("unsupported effort = %v", err)
	}
}
