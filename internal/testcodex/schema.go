package testcodex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// WriteSchema is a frozen capability projection of Codex 0.151.0, shared by
// deterministic child-process tests. It never inspects an installed runtime.
func WriteSchema(dir, mode string) {
	files := map[string]map[string][]string{
		"ClientRequest.json": {
			"initialize": {"clientInfo", "capabilities"}, "thread/start": {"cwd", "approvalPolicy", "approvalsReviewer", "config", "model", "sandbox"},
			"thread/read": {"threadId", "includeTurns"}, "thread/resume": {"threadId"},
			"turn/start": {"threadId", "input", "approvalPolicy", "approvalsReviewer", "cwd", "effort", "model", "sandboxPolicy"}, "turn/steer": {"threadId", "input", "expectedTurnId"},
			"turn/interrupt": {"threadId", "turnId"}, "account/read": {"refreshToken"},
			"account/rateLimits/read": {}, "model/list": {"cursor"}, "config/read": {"includeLayers"},
			"configRequirements/read": {}, "review/start": {"threadId", "target", "delivery"},
		},
		"ClientNotification.json": {"initialized": {}},
		"ServerRequest.json": {
			"item/commandExecution/requestApproval": {}, "item/fileChange/requestApproval": {},
			"item/permissions/requestApproval": {}, "item/tool/requestUserInput": {}, "mcpServer/elicitation/request": {},
		},
		"ServerNotification.json": {"turn/completed": {"threadId", "turn"}, "serverRequest/resolved": {"requestId", "threadId"}},
	}
	if mode == "missing-method" {
		delete(files["ClientRequest.json"], "turn/steer")
	}
	if mode == "missing-field" {
		files["ClientRequest.json"]["turn/steer"] = []string{"threadId", "input"}
	}
	for file, methods := range files {
		var variants []any
		definitions := map[string]any{
			"BooleanSchemaExample": map[string]any{"properties": map[string]any{"disabled": false}},
			"AskForApproval":       map[string]any{"enum": []string{"on-request", "never"}},
			"ApprovalsReviewer":    map[string]any{"enum": []string{"user", "auto_review"}},
			"SandboxMode":          map[string]any{"enum": []string{"read-only", "workspace-write"}},
		}
		if mode == "missing-policy-value" {
			definitions["ApprovalsReviewer"] = map[string]any{"enum": []string{"user"}}
		}
		for method, fields := range methods {
			name := strings.ReplaceAll(method, "/", "_") + "Params"
			properties := map[string]any{}
			for _, field := range fields {
				properties[field] = map[string]any{}
			}
			definitions[name] = map[string]any{"properties": properties}
			variants = append(variants, map[string]any{"properties": map[string]any{"method": map[string]any{"enum": []string{method}}, "params": map[string]any{"$ref": "#/definitions/" + name}}})
		}
		data, _ := json.Marshal(map[string]any{"oneOf": variants, "definitions": definitions})
		if err := os.WriteFile(filepath.Join(dir, file), data, 0600); err != nil {
			panic(err)
		}
	}
	responses := map[string]struct {
		fields      []string
		definitions map[string][]string
	}{
		"GetAccountResponse.json": {fields: []string{"account", "requiresOpenaiAuth"}},
		"ModelListResponse.json": {fields: []string{"data", "nextCursor"}, definitions: map[string][]string{
			"Model": {"id", "model", "displayName", "isDefault", "supportedReasoningEfforts"}, "ReasoningEffortOption": {"reasoningEffort"},
		}},
		"ConfigReadResponse.json": {fields: []string{"config"}, definitions: map[string][]string{
			"Config": {"model", "sandbox_workspace_write"}, "SandboxWorkspaceWrite": {"writable_roots", "network_access", "exclude_slash_tmp", "exclude_tmpdir_env_var"},
		}},
		"ConfigRequirementsReadResponse.json": {fields: []string{"requirements"}, definitions: map[string][]string{
			"ConfigRequirements": {"allowedApprovalPolicies", "allowedSandboxModes"},
		}},
	}
	v2 := filepath.Join(dir, "v2")
	if err := os.Mkdir(v2, 0700); err != nil {
		panic(err)
	}
	for file, response := range responses {
		properties := make(map[string]any, len(response.fields))
		for _, field := range response.fields {
			properties[field] = map[string]any{}
		}
		definitions := make(map[string]any, len(response.definitions))
		for name, fields := range response.definitions {
			definitionProperties := make(map[string]any, len(fields))
			for _, field := range fields {
				definitionProperties[field] = map[string]any{}
			}
			definitions[name] = map[string]any{"properties": definitionProperties}
		}
		if mode == "missing-response-field" && file == "ModelListResponse.json" {
			delete(definitions["ReasoningEffortOption"].(map[string]any)["properties"].(map[string]any), "reasoningEffort")
		}
		data, _ := json.Marshal(map[string]any{"properties": properties, "definitions": definitions})
		if err := os.WriteFile(filepath.Join(v2, file), data, 0600); err != nil {
			panic(err)
		}
	}
}
