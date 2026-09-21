package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

func TestPlanningUsesIsolatedAutoReviewShellAndExactCompletedOutput(t *testing.T) {
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
				Sandbox, ApprovalPolicy, ApprovalsReviewer, Model string
				Ephemeral                                         bool
				Config                                            map[string]json.RawMessage
			}
			if err := json.Unmarshal(call.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.Sandbox != "workspace-write" || params.ApprovalPolicy != "on-request" || params.ApprovalsReviewer != "auto_review" || !params.Ephemeral || params.Model != "test-fast" {
				t.Fatalf("thread permissions = %s", call.Params)
			}
			var features map[string]bool
			if json.Unmarshal(params.Config["features"], &features) != nil || len(features) < 10 {
				t.Fatal("tool capabilities missing")
			}
			for name, enabled := range features {
				if enabled != (name == "shell_tool" || name == "unified_exec") {
					t.Fatalf("unexpected planning capability %s=%v", name, enabled)
				}
			}
			if !features["shell_tool"] || !features["unified_exec"] {
				t.Fatal("planning shell is disabled")
			}
			var sandbox struct {
				WritableRoots       []string `json:"writable_roots"`
				NetworkAccess       bool     `json:"network_access"`
				ExcludeSlashTmp     bool     `json:"exclude_slash_tmp"`
				ExcludeTmpdirEnvVar bool     `json:"exclude_tmpdir_env_var"`
			}
			if json.Unmarshal(params.Config["sandbox_workspace_write"], &sandbox) != nil || sandbox.WritableRoots == nil || len(sandbox.WritableRoots) != 0 || sandbox.NetworkAccess || !sandbox.ExcludeSlashTmp || !sandbox.ExcludeTmpdirEnvVar {
				t.Fatalf("planning inherited broad shell access: %s", call.Params)
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
				ThreadID, Model, Effort, ApprovalPolicy, ApprovalsReviewer string
				SandboxPolicy                                              struct {
					Type                                 string
					NetworkAccess                        bool
					WritableRoots                        []string
					ExcludeSlashTmp, ExcludeTmpdirEnvVar bool
				}
				OutputSchema json.RawMessage
			}
			if err := json.Unmarshal(call.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID != "planning-thread" || params.Model != "test-fast" || params.Effort != "low" || params.ApprovalPolicy != "on-request" || params.ApprovalsReviewer != "auto_review" || params.SandboxPolicy.Type != "workspaceWrite" || params.SandboxPolicy.WritableRoots == nil || len(params.SandboxPolicy.WritableRoots) != 0 || !params.SandboxPolicy.ExcludeSlashTmp || !params.SandboxPolicy.ExcludeTmpdirEnvVar || params.SandboxPolicy.NetworkAccess || len(params.OutputSchema) == 0 {
				t.Fatalf("turn permissions/output = %s", call.Params)
			}
		case "response", "thread/resume", "thread/read", "turn/steer", "review/start":
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
		{"planning_interaction", ErrPlanningInteraction},
		{"planning_reviewer_blocked", ErrPolicyUnavailable},
		{"planning_sandbox_blocked", ErrPolicyUnavailable},
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

// Opt-in because this starts a real model turn through the configured account.
func TestInstalledPlanningShellAutomaticReview(t *testing.T) {
	if os.Getenv("PELLETS_CODEX_PLANNING_LIVE") != "1" {
		t.Skip("explicit live planning smoke test only; starts a model turn")
	}
	workspace := t.TempDir()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	evidence := "planning-shell-" + hex.EncodeToString(nonce[:])
	if err := os.WriteFile(filepath.Join(workspace, "planner-context.txt"), []byte(evidence), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	reply, err := Plan(ctx, PlanningOptions{WorkspaceDir: workspace, Prompt: "This is an authorized shell and automatic approval review smoke test. Use the shell with sandbox_permissions=require_escalated to run cat planner-context.txt in the current workspace, so the runtime can exercise automatic approval review for this harmless read. Do not modify anything. Return the exact contents of that file as your text and an empty drafts array. Do not guess the contents."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reply.Text, evidence) || len(reply.Drafts) != 0 {
		t.Fatalf("planner did not return shell evidence: %#v", reply)
	}
	t.Logf("live planner read unique shell evidence and returned valid structured output using %s/%s", reply.Model, reply.Effort)
}

type planningRejectSession struct {
	Session
	id     json.RawMessage
	result any
	rpc    *RPCError
}

func (s *planningRejectSession) Respond(_ context.Context, id json.RawMessage, result any, rpc *RPCError) error {
	s.id, s.result, s.rpc = id, result, rpc
	return nil
}

func TestPlanningNeverGrantsFallbackClientApprovals(t *testing.T) {
	for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput"} {
		t.Run(method, func(t *testing.T) {
			peer := &planningRejectSession{}
			session := &planningSession{Session: peer}
			id := json.RawMessage(`"request-one"`)
			err := session.handle(context.Background(), Event{Method: method, ID: id})
			if !errors.Is(err, ErrPlanningInteraction) || !errors.Is(err, ErrPlanningUnavailable) || string(peer.id) != string(id) || peer.result != nil || peer.rpc == nil || peer.rpc.Code != -32601 {
				t.Fatalf("fallback approval was not rejected: %#v, %v", peer, err)
			}
		})
	}
}

func TestPlanningFullAccessAndManagedRestriction(t *testing.T) {
	for _, mode := range []string{"planning_full", "planning_chat"} {
		t.Run(mode, func(t *testing.T) {
			opts := planningTestOptions(t, mode)
			opts.AccessMode = "full"
			_, err := Plan(context.Background(), opts)
			if mode == "planning_chat" {
				if !errors.Is(err, ErrPolicyUnavailable) {
					t.Fatalf("managed automatic-only policy accepted full access: %v", err)
				}
				for _, call := range planningRecordedCalls(t, opts.WorkspaceDir) {
					if call.Method == "thread/start" || call.Method == "turn/start" {
						t.Fatal("started work despite managed restriction")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, call := range planningRecordedCalls(t, opts.WorkspaceDir) {
				if call.Method != "thread/start" && call.Method != "turn/start" {
					continue
				}
				seen++
				var params map[string]any
				if err := json.Unmarshal(call.Params, &params); err != nil {
					t.Fatal(err)
				}
				if params["approvalPolicy"] != "never" || params["approvalsReviewer"] != "user" {
					t.Fatalf("full access params: %s", call.Params)
				}
				if call.Method == "thread/start" {
					if params["sandbox"] != "danger-full-access" || strings.Contains(params["developerInstructions"].(string), planningAutomaticInstructions) {
						t.Fatalf("full thread: %s", call.Params)
					}
				} else if params["sandboxPolicy"].(map[string]any)["type"] != "dangerFullAccess" {
					t.Fatalf("full turn: %s", call.Params)
				}
			}
			if seen != 2 {
				t.Fatalf("wanted thread and turn, got %d", seen)
			}
		})
	}
}

func TestGlobalModelsDoesNotReadWorkspaceConfiguration(t *testing.T) {
	options := planningTestOptions(t, "planning_chat")
	t.Chdir(options.WorkspaceDir)
	models, err := GlobalModels(context.Background())
	if err != nil || len(models) == 0 {
		t.Fatal(models, err)
	}
	for _, call := range planningRecordedCalls(t, options.WorkspaceDir) {
		switch call.Method {
		case "config/read", "configRequirements/read", "thread/start", "turn/start":
			t.Fatalf("global discovery called %s", call.Method)
		}
	}
}
