package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestNewExecutionPromptOrdersExactRawContextAndTask(t *testing.T) {
	for _, state := range []string{"captured", "empty", "ungrouped", "legacy"} {
		t.Run(state, func(t *testing.T) {
			run := storage.ExecutionRun{RunCapture: storage.RunCapture{PromptPrefix: storage.PromptPrefix{Text: "stable prefix\n"}}, GroupContext: storage.GroupContextSnapshot{Version: 1, State: state}}
			markdown := "# Exact Ω\r\n```mermaid\nflowchart LR\n A --> B\n```\n<script>never execute</script>\n"
			if state == "captured" || state == "empty" {
				if state == "empty" {
					markdown = ""
				}
				run.GroupContext.State = "captured"
				run.GroupContext.Group = &storage.GroupContext{ID: 42, Name: " name\nΩ ", Revision: 7, Context: markdown}
			}
			prompt, err := newExecutionPrompt(run, "exact task")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(prompt, "stable prefix\nCAPTURED GROUP CONTEXT v1\n") || !strings.HasSuffix(prompt, "\n\nexact task") {
				t.Fatalf("wrong ordering: %q", prompt)
			}
			if run.GroupContext.Group != nil {
				if !strings.Contains(prompt, `"id":42,"name":" name\nΩ ","revision":7`) || !strings.Contains(prompt, "\n"+markdown+"\nEND GROUP MARKDOWN sha256:") || !strings.Contains(prompt, "Do not execute Mermaid") {
					t.Fatalf("changed raw context: %q", prompt)
				}
			} else if strings.Contains(prompt, "BEGIN GROUP MARKDOWN") {
				t.Fatal("fabricated document")
			}
		})
	}
}

func TestExecutionPromptBoundsFailBeforeExternalIntent(t *testing.T) {
	run := storage.ExecutionRun{GroupContext: storage.GroupContextSnapshot{Version: 1, State: "ungrouped"}}
	if _, err := newExecutionPrompt(run, strings.Repeat("x", maxExecutionPromptBytes)); domain.PublicError(err).Code != "execution_prompt_too_large" {
		t.Fatalf("prompt overflow: %v", err)
	}
	recorder, database, selected, capture := executionFixture(t)
	run, err := recorder.Begin(context.Background(), database, selected, capture)
	if err != nil {
		t.Fatal(err)
	}
	session := evidenceSession{call: func(context.Context, codex.Operation, any) (json.RawMessage, error) {
		t.Fatal("oversized request reached runtime")
		return nil, nil
	}}
	// Escaping, rather than raw text size, takes this over the 4096-byte limit.
	_, _, err = recorder.CallCodex(context.Background(), database, run.ID, run.Revision, session, codex.TurnStart, map[string]any{"threadId": "thread", "input": strings.Repeat("\x01", 1000)})
	if domain.PublicError(err).Code != "execution_prompt_too_large" || !strings.Contains(err.Error(), "4096") {
		t.Fatalf("request overflow: %v", err)
	}
	after, err := recorder.Read(context.Background(), database, run.ID)
	if err != nil || after.Revision != run.Revision || after.PendingOperation != "" {
		t.Fatalf("oversized request recorded intent: %+v %v", after, err)
	}
}

func TestCheckpointPromptsUseCapturedGroupBeforeReadOnlyRole(t *testing.T) {
	run := storage.ExecutionRun{RunCapture: storage.RunCapture{PromptPrefix: storage.PromptPrefix{Text: "stable\n"}}, GroupContext: storage.GroupContextSnapshot{Version: 1, State: "captured", Group: &storage.GroupContext{ID: 1, Name: "review group", Revision: 4, Context: "# Original checkpoint context\n"}}}
	review, err := reviewPrompt(run, &storage.ReviewSnapshot{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	triage, err := checkpointTriagePrompt(run, storage.ReviewFinding{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for role, prompt := range map[string]string{"Review exactly": review, "Independently triage exactly": triage} {
		if !strings.HasPrefix(prompt, "stable\nCAPTURED GROUP CONTEXT v1\n") || strings.Count(prompt, run.GroupContext.Group.Context) != 1 || strings.Index(prompt, "END GROUP MARKDOWN") >= strings.Index(prompt, role) || !strings.Contains(strings.ToLower(prompt), "do not edit files") {
			t.Fatalf("checkpoint group context ordering or role changed: %s", prompt)
		}
	}
}
