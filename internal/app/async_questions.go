package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"pellets/internal/codex"
	"pellets/internal/storage"
)

// Async questions are agent-message content, not outstanding JSON-RPC requests.
// Answers are ordinary user input to the exact originating conversation.
func asyncQuestions(event codex.Event, run storage.ExecutionRun) (*storage.RunInteraction, error) {
	if event.Method != "item/completed" {
		return nil, nil
	}
	var p struct {
		ThreadID, TurnID string
		Item             struct {
			ID, Type  string
			Questions []struct {
				Title   string
				Options []string
			}
		}
	}
	if json.Unmarshal(event.Params, &p) != nil || p.ThreadID != run.ThreadID || p.TurnID != run.TurnID || p.Item.Type != "agentMessage" || len(p.Item.Questions) == 0 {
		return nil, nil
	}
	q := &storage.RunInteraction{RequestID: p.Item.ID, Method: storage.AsyncQuestionMethod, ThreadID: p.ThreadID, TurnID: p.TurnID, ItemID: p.Item.ID, Title: "Codex needs your input"}
	for i, v := range p.Item.Questions {
		question := storage.InteractionQuestion{ID: fmt.Sprintf("question-%d", i+1), Question: v.Title, Other: true, Required: true}
		for _, option := range v.Options {
			question.Options = append(question.Options, storage.InteractionOption{Label: option})
		}
		q.Questions = append(q.Questions, question)
	}
	return q, storage.ValidateRunInteraction(q)
}

func asyncAnswer(run storage.ExecutionRun, submission InteractionSubmission) (string, error) {
	q := run.Interaction
	if !storage.IsAsyncQuestion(q) || submission.RunID != run.ID || submission.Revision != run.Revision || submission.RequestID != q.RequestID || submission.Action != "answer" || len(submission.Answers) != len(q.Questions) {
		return "", storage.ExecutionRunConflict(run.ID)
	}
	var text strings.Builder
	text.WriteString("User answers to the pending questions (these answers apply only to the questions below):\n")
	for _, question := range q.Questions {
		values := submission.Answers[question.ID]
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" || len(values[0]) > maxInteractionAnswerBytes || !utf8.ValidString(values[0]) || strings.ContainsRune(values[0], 0) {
			return "", storage.InvalidExecutionRun("answer every displayed question")
		}
		fmt.Fprintf(&text, "\nQuestion: %s\nAnswer: %s\n", question.Question, values[0])
	}
	if text.Len() > 16384 {
		return "", storage.InvalidExecutionRun("combined answers exceed the input limit")
	}
	return text.String(), nil
}
