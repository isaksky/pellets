package testcodex

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// All planning content here is deterministic fixture output. Production calls
// the installed Codex model; this peer never contacts a model or reads secrets.
func handlePlanning(mode, method string, id, raw json.RawMessage, write func(any)) bool {
	if !strings.HasPrefix(mode, "planning_") {
		return false
	}
	respond := func(result any) { write(map[string]any{"id": id, "result": result}) }
	notify := func(method string, params any) { write(map[string]any{"method": method, "params": params}) }
	switch method {
	case "initialize":
		respond(map[string]any{"userAgent": "planning-test"})
	case "initialized":
	case "account/read":
		account := any(map[string]any{"type": "apiKey", "email": "private@example.test"})
		if mode == "planning_unsigned" {
			account = nil
		}
		respond(map[string]any{"account": account, "requiresOpenaiAuth": true})
	case "configRequirements/read":
		policy := "on-request"
		if mode == "planning_policy_blocked" {
			policy = "never"
		}
		reviewer, sandbox := "auto_review", "workspace-write"
		if mode == "planning_reviewer_blocked" {
			reviewer = "user"
		}
		if mode == "planning_sandbox_blocked" {
			sandbox = "read-only"
		}
		respond(map[string]any{"requirements": map[string]any{"allowedApprovalPolicies": []string{policy}, "allowedApprovalsReviewers": []string{reviewer}, "allowedSandboxModes": []string{sandbox}}})
	case "config/read":
		respond(map[string]any{"config": map[string]any{
			"model": "test-model", "model_reasoning_effort": "high",
			"mcp_servers": map[string]any{"fixture.external": map[string]any{"command": "never-run-me", "env": map[string]string{"API_KEY": "never-copy-me"}}},
		}})
	case "model/list":
		respond(map[string]any{"data": []any{
			map[string]any{"id": "test-model", "model": "test-model", "displayName": "Planning Test", "isDefault": true, "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "medium"}, map[string]any{"reasoningEffort": "high"}}},
			map[string]any{"id": "test-fast", "model": "test-fast", "displayName": "Planning Fast", "isDefault": false, "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"}}},
		}, "nextCursor": nil})
	case "thread/start":
		respond(map[string]any{"thread": map[string]any{"id": "planning-thread"}})
	case "turn/start":
		var params struct {
			Input []struct{ Text string } `json:"input"`
		}
		must(json.Unmarshal(raw, &params))
		must(os.WriteFile("fake-planning-started", []byte("ready"), 0600))
		if mode == "planning_gate" {
			waitFile("fake-planning-release")
		}
		if mode == "planning_rpc_error" {
			write(map[string]any{"id": id, "error": map[string]any{"code": -32000, "message": "private API_KEY=secret", "data": map[string]any{"token": "private"}}})
			return true
		}
		if mode == "planning_interaction" {
			write(map[string]any{"id": "planning-request", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "planning-thread", "turnId": "planning-turn"}})
			return true
		}
		if mode == "planning_disconnect" {
			os.Exit(9)
		}
		if mode == "planning_cancel" {
			time.Sleep(time.Minute)
			return true
		}
		text := params.Input[0].Text
		var context struct {
			Refine   string           `json:"refine_id"`
			Drafts   []map[string]any `json:"drafts"`
			Messages []struct {
				Text string `json:"text"`
			} `json:"messages"`
		}
		if start := strings.LastIndex(text, "\n\n{"); start >= 0 {
			_ = json.Unmarshal([]byte(text[start+2:]), &context)
		}
		drafts := []map[string]any{}
		assistant := "I drafted two focused pellets. Review them before creating work."
		if context.Refine != "" {
			for _, draft := range context.Drafts {
				if draft["id"] == context.Refine {
					drafts = append(drafts, map[string]any{"id": context.Refine, "title": draft["title"], "description": "Refined with the requested edge cases.", "acceptance": "Verify the requested edge cases with deterministic checks.", "group": draft["group"], "reason": "Refined from your follow-up."})
					assistant = "I refined the selected draft."
				}
			}
		} else {
			for _, title := range []string{"Validate planning input", "Preserve planning drafts"} {
				drafts = append(drafts, map[string]any{"id": "", "title": title, "description": "Implement the requested behavior using existing application state.", "acceptance": "Verify the behavior with a deterministic regression.", "group": "", "reason": "A distinct part of the requested plan."})
			}
		}
		if len(context.Messages) > 0 && strings.HasSuffix(strings.TrimSpace(context.Messages[len(context.Messages)-1].Text), "?") {
			drafts = []map[string]any{}
			assistant = "Review the drafts, select the work you want, then create it explicitly."
		}
		output, err := json.Marshal(map[string]any{"text": assistant, "drafts": drafts})
		must(err)
		if mode == "planning_bad_json" {
			output = []byte("This is not the required JSON output.")
		}
		if mode == "planning_oversized" {
			output = []byte(strings.Repeat("x", 300<<10))
		}
		// Shell output and runtime-owned review notifications must not become
		// the structured assistant reply or trigger client-side approval.
		notify("item/autoApprovalReview/started", map[string]any{"threadId": "planning-thread", "turnId": "planning-turn", "itemId": "command-one"})
		notify("item/autoApprovalReview/completed", map[string]any{"threadId": "planning-thread", "turnId": "planning-turn", "itemId": "command-one"})
		notify("item/completed", map[string]any{"threadId": "planning-thread", "turnId": "planning-turn", "item": map[string]any{"type": "commandExecution", "aggregatedOutput": "repository evidence"}})
		notify("item/completed", map[string]any{"threadId": "planning-thread", "turnId": "planning-turn", "item": map[string]any{"type": "agentMessage", "phase": "commentary", "text": "I inspected the relevant files."}})
		// A different thread/turn must not supply the planning result.
		notify("item/completed", map[string]any{"threadId": "unrelated-thread", "turnId": "planning-turn", "item": map[string]any{"type": "agentMessage", "text": "unrelated"}})
		notify("turn/completed", map[string]any{"threadId": "planning-thread", "turn": map[string]any{"id": "unrelated-turn", "status": "completed", "items": []any{map[string]any{"type": "agentMessage", "text": "unrelated"}}}})
		if mode != "planning_no_output" {
			notify("item/completed", map[string]any{"threadId": "planning-thread", "turnId": "planning-turn", "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": string(output)}})
		}
		status := "completed"
		if mode == "planning_failed" {
			status = "failed"
		}
		notify("turn/completed", map[string]any{"threadId": "planning-thread", "turn": map[string]any{"id": "planning-turn", "status": status}})
		// Exercise notifications arriving before the turn/start acknowledgement.
		respond(map[string]any{"turn": map[string]any{"id": "planning-turn"}})
	default:
		write(map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "unexpected planning operation"}})
	}
	return true
}
