package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"pellets/internal/codex"
	"pellets/internal/storage"
)

const maxInteractionAnswerBytes = 8192

// InteractionSubmission is bound to a rendered run revision and request ID.
// Answers are held only long enough to write the app-server response.
type InteractionSubmission struct {
	RunID     int64
	Revision  int64
	RequestID string
	Action    string
	Answers   map[string][]string
	FollowUp  string
}

type interactionAction struct {
	submission InteractionSubmission
	result     chan interactionResult
}

type interactionResult struct {
	run storage.ExecutionRun
	err error
}

func (supervisor *ExecutionSupervisor) SubmitInteraction(ctx context.Context, database Database, submission InteractionSubmission) (storage.ExecutionRun, error) {
	if submission.RunID < 1 || submission.Revision < 1 {
		return storage.ExecutionRun{}, storage.InvalidExecutionRun("run ID and revision must be positive")
	}
	supervisor.mu.Lock()
	execution := supervisor.active[activeExecutionKey{databasePath: database.Path, runID: submission.RunID}]
	supervisor.mu.Unlock()
	if execution == nil {
		return storage.ExecutionRun{}, scheduleError("run_process_unavailable", "the exact run process is no longer available; reload before continuing")
	}
	action := interactionAction{submission: submission, result: make(chan interactionResult, 1)}
	select {
	case execution.actions <- action:
	case <-ctx.Done():
		return storage.ExecutionRun{}, ctx.Err()
	case <-execution.ctx.Done():
		return storage.ExecutionRun{}, scheduleError("run_process_unavailable", "the exact run process is no longer available; reload before continuing")
	}
	select {
	case result := <-action.result:
		return result.run, result.err
	case <-ctx.Done():
		return storage.ExecutionRun{}, ctx.Err()
	case <-execution.ctx.Done():
		return storage.ExecutionRun{}, scheduleError("run_process_unavailable", "the exact run process stopped before the action was confirmed")
	}
}

func parseServerInteraction(event codex.Event, run storage.ExecutionRun) (*storage.RunInteraction, error) {
	requestID, err := canonicalRequestID(event.ID)
	if err != nil {
		return nil, err
	}
	base := struct {
		ThreadID string  `json:"threadId"`
		TurnID   string  `json:"turnId"`
		ItemID   string  `json:"itemId"`
		Reason   *string `json:"reason"`
	}{}
	if json.Unmarshal(event.Params, &base) != nil || base.ThreadID != run.ThreadID || (base.TurnID != "" && base.TurnID != run.TurnID) {
		return nil, storage.InvalidExecutionRun("Codex interaction does not match the exact run thread and turn")
	}
	interaction := &storage.RunInteraction{RequestID: requestID, Method: event.Method, ThreadID: base.ThreadID, TurnID: base.TurnID, ItemID: base.ItemID}
	if base.Reason != nil {
		interaction.Detail = boundedDisplay(*base.Reason, 4096)
	}
	switch event.Method {
	case "item/tool/requestUserInput":
		if base.ItemID == "" || base.TurnID == "" {
			return nil, storage.InvalidExecutionRun("Codex user-input request lacks its exact turn and item")
		}
		var params struct {
			Questions []struct {
				ID, Header, Question string
				Options              []storage.InteractionOption `json:"options"`
				IsOther, IsSecret    bool
			} `json:"questions"`
		}
		if json.Unmarshal(event.Params, &params) != nil || len(params.Questions) == 0 {
			return nil, storage.InvalidExecutionRun("Codex user-input request is malformed")
		}
		interaction.Title = "Codex needs your input"
		for _, question := range params.Questions {
			interaction.Questions = append(interaction.Questions, storage.InteractionQuestion{ID: question.ID, Header: question.Header, Question: question.Question, Options: question.Options, Other: question.IsOther, Secret: question.IsSecret, Required: true})
		}
	case "item/commandExecution/requestApproval":
		if base.ItemID == "" || base.TurnID == "" {
			return nil, storage.InvalidExecutionRun("Codex approval request lacks its exact turn and item")
		}
		interaction.Title = "Approve this one command?"
		var params struct {
			Kind    string                           `json:"kind"`
			Network *struct{ Host, Protocol string } `json:"networkApprovalContext"`
		}
		_ = json.Unmarshal(event.Params, &params)
		if params.Kind == "writeStdin" {
			interaction.Title = "Approve input to the running command?"
		}
		if params.Network != nil {
			interaction.Title = "Approve this one network request?"
			interaction.Detail = strings.TrimSpace(interaction.Detail + " " + params.Network.Protocol + "://" + params.Network.Host)
		}
	case "item/fileChange/requestApproval":
		if base.ItemID == "" || base.TurnID == "" {
			return nil, storage.InvalidExecutionRun("Codex approval request lacks its exact turn and item")
		}
		interaction.Title = "Approve this one file change?"
	case "item/permissions/requestApproval":
		if base.ItemID == "" || base.TurnID == "" {
			return nil, storage.InvalidExecutionRun("Codex permissions request lacks its exact turn and item")
		}
		interaction.Title = "Grant these permissions for this turn?"
		var params struct {
			Permissions json.RawMessage `json:"permissions"`
		}
		if json.Unmarshal(event.Params, &params) != nil || len(params.Permissions) == 0 || bytes.Equal(bytes.TrimSpace(params.Permissions), []byte("null")) {
			return nil, storage.InvalidExecutionRun("Codex permissions request is malformed")
		}
		compact := &bytes.Buffer{}
		if json.Compact(compact, params.Permissions) != nil || compact.Len() > 3500 {
			return nil, storage.InvalidExecutionRun("Codex permissions request is too large to present completely")
		}
		interaction.RequestedPermissions = append(json.RawMessage(nil), compact.Bytes()...)
		interaction.Detail = "Requested permissions: " + compact.String()
	case "mcpServer/elicitation/request":
		var params struct {
			Mode, Message, ServerName, URL string
			RequestedSchema                struct {
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			} `json:"requestedSchema"`
		}
		if json.Unmarshal(event.Params, &params) != nil || params.ServerName == "" || params.Message == "" {
			return nil, storage.InvalidExecutionRun("MCP elicitation request is malformed")
		}
		interaction.ItemID = ""
		interaction.Title = boundedDisplay(params.Message, 1024)
		switch params.Mode {
		case "url":
			parsed, err := url.Parse(params.URL)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
				return nil, storage.InvalidExecutionRun("MCP elicitation URL is invalid")
			}
			interaction.Mode, interaction.Detail = "url", params.URL
		case "form":
			interaction.Mode = "form"
			required := map[string]bool{}
			for _, name := range params.RequestedSchema.Required {
				required[name] = true
			}
			names := make([]string, 0, len(params.RequestedSchema.Properties))
			for name := range params.RequestedSchema.Properties {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				var schema struct {
					Type, Title, Description string
					Enum                     []string                        `json:"enum"`
					OneOf                    []struct{ Const, Title string } `json:"oneOf"`
				}
				if json.Unmarshal(params.RequestedSchema.Properties[name], &schema) != nil || (schema.Type != "string" && schema.Type != "number" && schema.Type != "integer" && schema.Type != "boolean") {
					return nil, storage.InvalidExecutionRun("MCP elicitation contains an unsupported form field")
				}
				question := storage.InteractionQuestion{ID: name, Header: schema.Title, Question: schema.Description, Type: schema.Type, Required: required[name]}
				if question.Question == "" {
					question.Question = name
				}
				for _, value := range schema.Enum {
					question.Options = append(question.Options, storage.InteractionOption{Label: value})
				}
				for _, value := range schema.OneOf {
					question.Options = append(question.Options, storage.InteractionOption{Label: value.Const, Description: value.Title})
				}
				if schema.Type == "boolean" {
					question.Options = []storage.InteractionOption{{Label: "true", Description: "Yes"}, {Label: "false", Description: "No"}}
				}
				interaction.Questions = append(interaction.Questions, question)
			}
		default:
			return nil, storage.InvalidExecutionRun("MCP elicitation mode is not supported")
		}
	default:
		return nil, codex.ErrUnsupported
	}
	if err := storage.ValidateRunInteraction(interaction); err != nil {
		return nil, err
	}
	return interaction, nil
}

func canonicalRequestID(raw json.RawMessage) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if len(raw) == 0 || decoder.Decode(&value) != nil || decoder.Decode(new(any)) == nil {
		return "", storage.InvalidExecutionRun("invalid Codex request ID")
	}
	switch value.(type) {
	case string, json.Number:
	default:
		return "", storage.InvalidExecutionRun("invalid Codex request ID")
	}
	compact := &bytes.Buffer{}
	if json.Compact(compact, raw) != nil || compact.Len() > 512 {
		return "", storage.InvalidExecutionRun("invalid Codex request ID")
	}
	return compact.String(), nil
}

func boundedDisplay(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (execution *WorkspaceExecution) applyInteraction(ctx context.Context, action interactionAction) (storage.ExecutionRun, bool, error) {
	submission := action.submission
	run, err := execution.Read(ctx)
	if err != nil {
		return run, false, err
	}
	if run.ID != submission.RunID || run.Revision != submission.Revision || !storage.RunActive(run.State) {
		return run, false, storage.ExecutionRunConflict(submission.RunID)
	}
	if submission.FollowUp != "" {
		return execution.applyFollowUp(ctx, run, submission.FollowUp)
	}
	interaction := run.Interaction
	if interaction == nil || submission.RequestID != interaction.RequestID || submission.Action == "" {
		return run, false, storage.ExecutionRunConflict(run.ID)
	}
	var response any
	switch interaction.Method {
	case "item/tool/requestUserInput":
		if submission.Action != "answer" {
			return run, false, storage.InvalidExecutionRun("choose answers for this request")
		}
		answers := make(map[string]any, len(interaction.Questions))
		for _, question := range interaction.Questions {
			values := submission.Answers[question.ID]
			if len(values) == 0 || len(values) > 32 {
				return run, false, storage.InvalidExecutionRun("answer every displayed question")
			}
			for _, value := range values {
				if value == "" || len(value) > maxInteractionAnswerBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) || !allowedAnswer(question, value) {
					return run, false, storage.InvalidExecutionRun("an answer is invalid for the displayed question")
				}
			}
			answers[question.ID] = map[string]any{"answers": values}
		}
		if len(submission.Answers) != len(interaction.Questions) {
			return run, false, storage.InvalidExecutionRun("answers do not match the displayed questions")
		}
		response = map[string]any{"answers": answers}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if submission.Action != "accept" && submission.Action != "decline" {
			return run, false, storage.InvalidExecutionRun("choose a one-time approval outcome")
		}
		response = map[string]any{"decision": submission.Action}
	case "item/permissions/requestApproval":
		if submission.Action != "accept" && submission.Action != "decline" {
			return run, false, storage.InvalidExecutionRun("choose a turn-scoped permission outcome")
		}
		permissions := any(map[string]any{})
		if submission.Action == "accept" {
			if json.Unmarshal(interaction.RequestedPermissions, &permissions) != nil {
				return run, false, storage.InvalidExecutionRun("stored permissions are invalid")
			}
		}
		response = map[string]any{"permissions": permissions, "scope": "turn", "strictAutoReview": true}
	case "mcpServer/elicitation/request":
		if submission.Action == "decline" {
			response = map[string]any{"action": "decline", "content": nil}
			break
		}
		if submission.Action != "accept" {
			return run, false, storage.InvalidExecutionRun("choose an MCP elicitation outcome")
		}
		content := map[string]any{}
		if interaction.Mode == "form" {
			expected := make(map[string]bool, len(interaction.Questions))
			for _, question := range interaction.Questions {
				expected[question.ID] = true
				values := submission.Answers[question.ID]
				if len(values) == 0 || values[0] == "" {
					if question.Required {
						return run, false, storage.InvalidExecutionRun("answer every required displayed field")
					}
					continue
				}
				if len(values) != 1 || len(values[0]) > maxInteractionAnswerBytes || !allowedAnswer(question, values[0]) {
					return run, false, storage.InvalidExecutionRun("an answer is invalid for the displayed field")
				}
				value, err := typedAnswer(question.Type, values[0])
				if err != nil {
					return run, false, storage.InvalidExecutionRun("an answer has the wrong displayed type")
				}
				content[question.ID] = value
			}
			for name := range submission.Answers {
				if !expected[name] {
					return run, false, storage.InvalidExecutionRun("answers do not match the displayed MCP fields")
				}
			}
		} else if len(submission.Answers) != 0 {
			return run, false, storage.InvalidExecutionRun("this MCP decision does not accept form fields")
		}
		response = map[string]any{"action": "accept", "content": content}
	default:
		return run, false, codex.ErrUnsupported
	}
	if err := execution.Respond(ctx, json.RawMessage(interaction.RequestID), response, nil); err != nil {
		return run, false, err
	}
	progress := run.RunProgress
	progress.State, progress.Interaction, progress.ErrorCode = "running", nil, ""
	if submission.Action == "decline" {
		progress.Summary = "The requested action was denied; Codex is continuing without broader permission."
	} else {
		progress.Summary = "The exact pending response was delivered; Codex is continuing."
	}
	run, err = execution.Save(ctx, progress, run.Revision)
	return run, false, err
}

func typedAnswer(kind, value string) (any, error) {
	switch kind {
	case "", "string":
		return value, nil
	case "number":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return nil, fmt.Errorf("invalid finite number")
		}
		return parsed, nil
	case "integer":
		return strconv.ParseInt(value, 10, 64)
	case "boolean":
		return strconv.ParseBool(value)
	default:
		return nil, fmt.Errorf("unsupported answer type %q", kind)
	}
}

func allowedAnswer(question storage.InteractionQuestion, value string) bool {
	if len(question.Options) == 0 || question.Other {
		return true
	}
	for _, option := range question.Options {
		if value == option.Label {
			return true
		}
	}
	return false
}

func (execution *WorkspaceExecution) applyFollowUp(ctx context.Context, run storage.ExecutionRun, text string) (storage.ExecutionRun, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 16384 || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return run, false, storage.InvalidExecutionRun("follow-up instructions must be concise UTF-8 text")
	}
	if run.Interaction != nil {
		return run, false, storage.ExecutionRunConflict(run.ID)
	}
	input := []any{map[string]any{"type": "text", "text": text}}
	terminal := completedTurn(execution.LatestCompletion(), run.ThreadID, run.TurnID)
	if !terminal {
		response, err := execution.Call(ctx, codex.TurnSteer, map[string]any{"threadId": run.ThreadID, "expectedTurnId": run.TurnID, "input": input})
		if err == nil {
			var result struct {
				TurnID string `json:"turnId"`
			}
			if json.Unmarshal(response, &result) != nil || result.TurnID != run.TurnID {
				return run, false, codex.ErrProtocol
			}
			progress := run.RunProgress
			progress.Summary = "Follow-up instructions were steered into the active turn."
			run, err = execution.Save(ctx, progress, run.Revision)
			return run, true, err
		}
		if !completedTurn(execution.LatestCompletion(), run.ThreadID, run.TurnID) {
			return run, false, errors.Join(scheduleError("follow_up_not_accepted", "the active turn could not accept follow-up instructions"), err)
		}
	}
	params, err := execution.TurnStartParams(run.ThreadID, input)
	if err != nil {
		return run, false, err
	}
	params["outputSchema"] = implementationSchema()
	if _, err = execution.Call(ctx, codex.TurnStart, params); err != nil {
		return run, false, err
	}
	run, err = execution.Read(ctx)
	if err != nil {
		return run, false, err
	}
	progress := run.RunProgress
	progress.Phase, progress.State, progress.Summary = "implementation", "running", "Follow-up instructions started a new turn after the prior turn became idle."
	run, err = execution.Save(ctx, progress, run.Revision)
	return run, true, err
}

func resolvedRequestID(event codex.Event) string {
	if event.Method != "serverRequest/resolved" {
		return ""
	}
	var params struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(event.Params, &params) != nil {
		return ""
	}
	id, _ := canonicalRequestID(params.RequestID)
	return fmt.Sprintf("%s\x00%s", params.ThreadID, id)
}
