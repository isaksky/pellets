// Package testcodex contains deterministic external-process integration peers.
// It is imported only by tests, never by the Pellets executable.
package testcodex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Run handles child invocations of a test binary; ordinary test dispatch falls
// through. No installed Codex, credentials, model, or live database is touched.
func Run() bool {
	if os.Getenv("PELLETS_SUPERVISOR_PEER") != "1" || len(os.Args) < 2 {
		return false
	}
	args := os.Args[1:]
	if strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe") == "pl" {
		if len(args) > 0 && (args[0] == "--version" || args[len(args)-1] == "--help") {
			if args[0] == "--version" {
				version := os.Getenv("PELLETS_SUPERVISOR_PEER_VERSION")
				if version == "" {
					version = "supervisor-test"
				}
				fmt.Println("pl " + version)
			} else {
				fmt.Printf("Usage: pl %s\n", strings.Join(args[:len(args)-1], " "))
			}
			return true
		}
		return false
	}
	if args[0] == "--version" {
		fmt.Println("codex-cli 0.151.0")
		return true
	}
	if args[0] == "--fake-descendant" {
		record("process", nil)
		if args[1] == "child" {
			spawn("grandchild")
			waitFile("fake-grandchild-ready")
		}
		must(os.WriteFile("fake-"+args[1]+"-ready", []byte("ready"), 0600))
		time.Sleep(time.Minute)
		return true
	}
	if args[0] != "app-server" {
		return false
	}
	if args[1] == "generate-json-schema" {
		WriteSchema(args[3], "")
		return true
	}
	modeBytes, _ := os.ReadFile("fake-mode")
	mode := string(modeBytes)
	record("process", nil)
	scanner := bufio.NewScanner(os.Stdin)
	write := func(value any) { must(json.NewEncoder(os.Stdout).Encode(value)) }
	var scheduledTurn json.RawMessage
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		must(json.Unmarshal(scanner.Bytes(), &message))
		if message.Method == "" && len(message.ID) != 0 {
			record("response", scanner.Bytes())
			if mode == "schedule_input_live" || mode == "schedule_approval_live" {
				write(map[string]any{"method": "serverRequest/resolved", "params": map[string]any{"threadId": "thread", "requestId": json.RawMessage(message.ID)}})
				writeScheduledCompletion(write, mode, scheduledTurn)
			}
			continue
		}
		record(message.Method, message.Params)
		var result any = map[string]any{}
		switch message.Method {
		case "initialize":
			result = map[string]any{"userAgent": "supervisor-test"}
		case "initialized":
			continue
		case "account/read":
			if mode == "unauthenticated" {
				result = map[string]any{"account": nil, "requiresOpenaiAuth": true}
			} else {
				result = map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}
			}
		case "configRequirements/read":
			result = map[string]any{"requirements": nil}
		case "config/read":
			result = map[string]any{"config": map[string]any{"model": "test-model"}}
		case "model/list":
			result = map[string]any{"data": []any{map[string]any{"id": "test-model", "model": "test-model", "displayName": "Test", "isDefault": true, "supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "high"}}}}, "nextCursor": nil}
		case "thread/start", "thread/resume":
			if mode == "schedule_prethread_failure" && message.Method == "thread/start" {
				result = map[string]any{}
			} else if strings.HasPrefix(mode, "review_") && message.Method == "thread/start" {
				result = map[string]any{"thread": map[string]any{"id": "review-seed"}}
			} else {
				result = map[string]any{"thread": map[string]any{"id": "thread"}}
			}
		case "turn/start":
			if !strings.HasPrefix(mode, "schedule_") {
				spawn("child")
				waitFile("fake-child-ready")
			}
			result = map[string]any{"turn": map[string]any{"id": "turn"}}
			scheduledTurn = append(scheduledTurn[:0], message.Params...)
		case "turn/steer":
			result = map[string]any{"turnId": "turn"}
		case "thread/read":
			if mode == "crash" {
				os.Exit(9)
			}
			cwd, err := os.Getwd()
			must(err)
			threadID, turnID := "thread", "turn"
			if strings.HasPrefix(mode, "review_") {
				threadID, turnID = "review-thread", "review-turn"
			}
			result = map[string]any{"thread": map[string]any{"id": threadID, "cwd": cwd, "status": map[string]any{"type": "notLoaded"}, "turns": []any{map[string]any{"id": turnID, "status": "completed"}}}}
			if mode == "schedule_missing_history" {
				result = map[string]any{}
			}
			if mode == "schedule_active_history" {
				result = map[string]any{"thread": map[string]any{"id": "thread", "cwd": cwd, "status": map[string]any{"type": "active"}, "turns": []any{}}}
			}
			if mode == "schedule_advanced_history" {
				result = map[string]any{"thread": map[string]any{"id": "thread", "cwd": cwd, "status": map[string]any{"type": "notLoaded"}, "turns": []any{map[string]any{"id": "turn", "status": "completed"}, map[string]any{"id": "external-turn", "status": "completed"}}}}
			}
		case "review/start":
			reviewThreadID := "review-thread"
			if mode == "review_same_context" {
				reviewThreadID = "review-seed"
			}
			result = map[string]any{"reviewThreadId": reviewThreadID, "turn": map[string]any{"id": "review-turn", "status": "inProgress", "items": []any{}}}
			scheduledTurn = append(scheduledTurn[:0], message.Params...)
		case "turn/interrupt":
			if mode == "hang-interrupt" {
				continue
			}
			write(map[string]any{"id": message.ID, "result": result})
			if mode != "forced" {
				write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "interrupted"}}})
			}
			continue
		}
		write(map[string]any{"id": message.ID, "result": result})
		if message.Method == "review/start" && strings.HasPrefix(mode, "review_") {
			if mode == "review_gate" {
				waitFile("fake-complete")
			}
			if mode == "review_side_effect" {
				must(os.WriteFile("review-side-effect.txt", []byte("changed"), 0600))
			}
			if mode == "review_dirty_side_effect" {
				must(os.WriteFile("code-1.txt", []byte("dirty-after\n"), 0600))
			}
			write(map[string]any{"method": "item/started", "params": map[string]any{"threadId": "review-thread", "turnId": "review-turn", "item": map[string]any{"type": "enteredReviewMode", "id": "review-turn", "review": "exact checkpoint"}}})
			if mode == "review_interaction" {
				write(map[string]any{"id": "review-request", "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "review-thread", "turnId": "review-turn", "itemId": "review-item", "questions": []any{map[string]any{"id": "permission", "header": "Permission", "question": "Change external state?", "options": []any{map[string]any{"label": "Yes", "description": "Mutate state"}}}}}})
			}
			if mode != "review_missing_result" {
				review := cleanReviewMarker(scheduledTurn)
				if mode == "review_findings" {
					cwd, err := os.Getwd()
					must(err)
					review = "The exact changes have one correctness issue.\n\nReview comment:\n\n- [P1] Handle edge case — " + filepath.Join(cwd, "demo-1.txt") + ":1-1\n  The selected change misses the empty input case."
				}
				if mode == "review_malformed_result" {
					review = "Reviewer failed to output a response."
				}
				if mode == "review_json_result" {
					review = `{"version":1,"status":"clean","summary":"invented wire contract","findings":[]}`
				}
				if mode == "review_fallback_prose" {
					review = "The model returned prose that could not be parsed as ReviewOutputEvent."
				}
				if mode == "review_truncated_json" {
					review = `{"findings":[],"overall_correctness":"patch is correct"`
				}
				if mode == "review_fenced_malformed" {
					review = "```json\n{\"findings\": []}\n```"
				}
				write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "review-thread", "turnId": "review-turn", "item": map[string]any{"type": "exitedReviewMode", "id": "review-turn", "review": review}}})
			}
			if mode == "review_result_gate" {
				waitFile("fake-complete")
			}
			turnStatus := "completed"
			if mode == "review_failed" {
				turnStatus = "failed"
			}
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "review-thread", "turn": map[string]any{"id": "review-turn", "status": turnStatus}}})
			continue
		}
		if message.Method == "turn/steer" && mode == "schedule_followup_live" {
			writeScheduledCompletion(write, mode, scheduledTurn)
			continue
		}
		if message.Method == "turn/start" && strings.HasPrefix(mode, "schedule_") {
			if mode == "schedule_exit" {
				os.Exit(9)
			}
			if mode == "schedule_input_live" {
				write(map[string]any{"id": 41, "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "question-item", "isBlocking": true, "questions": []any{map[string]any{"id": "choice", "header": "Scope", "question": "Which scope should be used?", "options": []any{map[string]any{"label": "Focused", "description": "Change only the target."}, map[string]any{"label": "Broad", "description": "Change related code."}}, "isOther": true, "isSecret": false}, map[string]any{"id": "note", "header": "Note", "question": "Any concise constraint?", "options": nil, "isOther": false, "isSecret": false}}}})
				continue
			}
			if mode == "schedule_approval_live" {
				write(map[string]any{"id": "approval-7", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "command-item", "startedAtMs": 1, "kind": "command", "reason": "The sandbox requires a one-time decision."}})
				continue
			}
			if mode == "schedule_followup_live" {
				continue
			}
			if mode == "schedule_gate" {
				waitFile("fake-complete")
			}
			report := completeScheduled(mode, message.Params)
			if mode != "schedule_turn_only" {
				write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": report}}})
			}
			write(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "thread", "turnId": "turn", "tokenUsage": map[string]any{"last": map[string]any{"cachedInputTokens": int64(7)}, "total": map[string]any{"cachedInputTokens": int64(70)}}}})
			if mode == "schedule_input" {
				write(map[string]any{"id": 1, "method": "item/tool/requestUserInput", "params": map[string]any{}})
				continue
			}
			if mode == "schedule_approval" {
				write(map[string]any{"id": 1, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
				continue
			}
			status := "completed"
			if mode == "schedule_failed" {
				status = "failed"
			}
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": status}}})
		}
	}
	return true
}

func cleanReviewMarker(params json.RawMessage) string {
	var request struct {
		Target struct {
			Instructions string `json:"instructions"`
		} `json:"target"`
	}
	must(json.Unmarshal(params, &request))
	const prefix = "PELLETS_REVIEW_CLEAN_V1 sha256:"
	start := strings.Index(request.Target.Instructions, prefix)
	if start < 0 || len(request.Target.Instructions) < start+len(prefix)+64 {
		panic("review request lacks its scope-bound clean marker")
	}
	return request.Target.Instructions[start : start+len(prefix)+64]
}

func writeScheduledCompletion(write func(any), mode string, params json.RawMessage) {
	report := completeScheduled(mode, params)
	write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"type": "agentMessage", "phase": "final_answer", "text": report}}})
	write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}}})
}

func record(method string, params json.RawMessage) {
	root, err := os.Getwd()
	must(err)
	root, err = filepath.EvalSymlinks(root)
	must(err)
	data, err := json.Marshal(map[string]any{"pid": os.Getpid(), "cwd": root, "method": method, "params": params})
	must(err)
	file, err := os.OpenFile("fake-events.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	must(err)
	_, err = file.Write(append(data, '\n'))
	must(err)
	must(file.Close())
}

func spawn(role string) {
	executable, err := os.Executable()
	must(err)
	cmd := exec.Command(executable, "--fake-descendant", role)
	Detach(cmd)
	must(cmd.Start())
	go cmd.Wait()
}

func waitFile(path string) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			panic("fake descendant readiness timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
