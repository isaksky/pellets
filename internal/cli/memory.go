package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/output"
	"pellets/internal/storage"
)

// MemoryCommand implements the project-memory command family.
func MemoryCommand(manager app.MemoryManager) Command {
	return Command{
		Name:    "memory",
		Summary: "Add, search, review, or remove project memory.",
		Usage: "pl memory add (--text TEXT | --file PATH) [--created-by agent|human]\n" +
			"  pl memory list [--approved-only] [--limit N]\n" +
			"  pl memory show MEMORY_ID\n" +
			"  pl memory search QUERY [--approved-only] [--limit N]\n" +
			"  pl memory approve MEMORY_ID\n" +
			"  pl memory remove MEMORY_ID --yes\n\n" +
			"Memory text must be non-empty valid UTF-8 and at most 1,048,576 bytes.",
		Parse: parseMemory,
		ResultName: func(value any) string {
			return "memory " + value.(memoryInput).Action
		},
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(memoryInput)
			database := invocationDatabase(invocation)
			switch input.Action {
			case "add":
				text, err := resolveMemoryText(input.Text, input.File, invocation.Stdin, invocation.WorkingDirectory)
				if err != nil {
					return nil, err
				}
				memory, err := manager.Add(
					ctx, database, invocation.WorkingDirectory, invocation.Globals.Project,
					storage.NewMemory{Text: text, CreatedBy: input.CreatedBy},
				)
				if err != nil {
					return nil, err
				}
				return newMemoryData(memory), nil
			case "list":
				memories, err := manager.List(
					ctx, database, invocation.WorkingDirectory, invocation.Globals.Project,
					storage.MemoryListOptions{ApprovedOnly: input.ApprovedOnly, Limit: input.Limit},
				)
				if err != nil {
					return nil, err
				}
				result := make(memoryListData, len(memories))
				for index, memory := range memories {
					result[index] = newMemoryData(memory)
				}
				return result, nil
			case "search":
				memories, err := manager.Search(
					ctx, database, invocation.WorkingDirectory, invocation.Globals.Project,
					storage.MemorySearchOptions{
						Query: input.Query, ApprovedOnly: input.ApprovedOnly, Limit: input.Limit,
					},
				)
				if err != nil {
					return nil, err
				}
				result := make(memoryListData, len(memories))
				for index, memory := range memories {
					result[index] = newMemorySearchData(memory)
				}
				return result, nil
			case "show":
				memory, err := manager.Show(ctx, database, invocation.WorkingDirectory, invocation.Globals.Project, input.ID)
				if err != nil {
					return nil, err
				}
				return newMemoryData(memory), nil
			case "approve":
				memory, err := manager.Approve(ctx, database, invocation.WorkingDirectory, invocation.Globals.Project, input.ID)
				if err != nil {
					return nil, err
				}
				return newMemoryData(memory), nil
			case "remove":
				memory, err := manager.Remove(ctx, database, invocation.WorkingDirectory, invocation.Globals.Project, input.ID)
				if err != nil {
					return nil, err
				}
				return newMemoryData(memory), nil
			default:
				return nil, domain.NewError(domain.Unexpected, "internal_error", "unknown parsed memory action", nil)
			}
		},
	}
}

type memoryInput struct {
	Action       string
	Text         *string
	File         *string
	CreatedBy    domain.MemoryCreator
	ApprovedOnly bool
	Limit        *int64
	Query        string
	ID           int64
}

func parseMemory(args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.NewError(domain.Usage, "missing_subcommand", "memory requires add, list, show, search, approve, or remove", map[string]any{"command": "memory"})
	}
	action := args[0]
	if strings.HasPrefix(action, "-") {
		return nil, unknownFlag(action)
	}
	input := memoryInput{Action: action}
	var err error
	switch action {
	case "add":
		input, err = parseMemoryAdd(args[1:])
	case "list":
		input, err = parseMemoryList(args[1:])
	case "search":
		input, err = parseMemorySearch(args[1:])
	case "show", "approve":
		input.ID, err = parseMemoryIDArgument(action, args[1:])
	case "remove":
		input, err = parseMemoryRemove(args[1:])
	default:
		return nil, domain.NewError(
			domain.Usage,
			"unknown_subcommand",
			fmt.Sprintf("unknown memory subcommand %q", action),
			map[string]any{"command": "memory", "subcommand": action},
		)
	}
	if err != nil {
		return nil, err
	}
	input.Action = action
	return input, nil
}

func parseMemoryAdd(args []string) (memoryInput, error) {
	input := memoryInput{CreatedBy: domain.MemoryCreatedByAgent}
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			return memoryInput{}, unexpectedArgument(argument)
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return memoryInput{}, duplicateCommandFlag(name)
		}
		seen[name] = true
		if name != "--text" && name != "--file" && name != "--created-by" {
			return memoryInput{}, unknownFlag(name)
		}
		var err error
		value, args, err = takeCommandFlagValue(args, name, value, hasValue, name == "--file", name == "--text")
		if err != nil {
			return memoryInput{}, err
		}
		switch name {
		case "--text":
			input.Text = stringPointer(value)
		case "--file":
			input.File = stringPointer(value)
		case "--created-by":
			input.CreatedBy = domain.MemoryCreator(value)
			if err := domain.ValidateMemoryCreator(input.CreatedBy); err != nil {
				return memoryInput{}, err
			}
		}
	}
	if input.Text == nil && input.File == nil {
		return memoryInput{}, domain.NewError(
			domain.Usage, "missing_memory_text", "memory add requires exactly one of --text or --file",
			map[string]any{"flags": []string{"--text", "--file"}},
		)
	}
	if input.Text != nil && input.File != nil {
		return memoryInput{}, conflictingFlags("--text", "--file")
	}
	if input.Text != nil {
		if err := domain.ValidateMemoryText(*input.Text); err != nil {
			return memoryInput{}, err
		}
	}
	return input, nil
}

func parseMemoryList(args []string) (memoryInput, error) {
	var input memoryInput
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			return memoryInput{}, unexpectedArgument(argument)
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return memoryInput{}, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--approved-only":
			if hasValue {
				return memoryInput{}, flagTakesNoValue(name)
			}
			input.ApprovedOnly = true
			args = args[1:]
		case "--limit":
			var err error
			value, args, err = takeCommandFlagValue(args, name, value, hasValue, false, false)
			if err != nil {
				return memoryInput{}, err
			}
			limit, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || limit <= 0 || strconv.FormatInt(limit, 10) != value {
				return memoryInput{}, invalidLimit(value)
			}
			input.Limit = &limit
		default:
			return memoryInput{}, unknownFlag(name)
		}
	}
	return input, nil
}

func parseMemorySearch(args []string) (memoryInput, error) {
	var input memoryInput
	querySet := false
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			if querySet {
				return memoryInput{}, unexpectedArgument(argument)
			}
			if strings.TrimSpace(argument) == "" {
				return memoryInput{}, domain.NewError(domain.Usage, "missing_query", "memory search requires a non-empty QUERY", nil)
			}
			input.Query = argument
			querySet = true
			args = args[1:]
			continue
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return memoryInput{}, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--approved-only":
			if hasValue {
				return memoryInput{}, flagTakesNoValue(name)
			}
			input.ApprovedOnly = true
			args = args[1:]
		case "--limit":
			var err error
			value, args, err = takeCommandFlagValue(args, name, value, hasValue, false, false)
			if err != nil {
				return memoryInput{}, err
			}
			limit, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || limit <= 0 || strconv.FormatInt(limit, 10) != value {
				return memoryInput{}, invalidLimit(value)
			}
			input.Limit = &limit
		default:
			return memoryInput{}, unknownFlag(name)
		}
	}
	if !querySet {
		return memoryInput{}, domain.NewError(domain.Usage, "missing_query", "memory search requires a non-empty QUERY", nil)
	}
	return input, nil
}

func parseMemoryRemove(args []string) (memoryInput, error) {
	var input memoryInput
	memoryIDSet := false
	yes := false
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			if memoryIDSet {
				return memoryInput{}, unexpectedArgument(argument)
			}
			memoryID, err := domain.ParseMemoryID(argument)
			if err != nil {
				return memoryInput{}, err
			}
			input.ID = memoryID
			memoryIDSet = true
			args = args[1:]
			continue
		}
		name, _, hasValue := splitOption(argument)
		if seen[name] {
			return memoryInput{}, duplicateCommandFlag(name)
		}
		seen[name] = true
		if name != "--yes" {
			return memoryInput{}, unknownFlag(name)
		}
		if hasValue {
			return memoryInput{}, flagTakesNoValue(name)
		}
		yes = true
		args = args[1:]
	}
	if !memoryIDSet {
		return memoryInput{}, domain.NewError(domain.Usage, "missing_memory_id", "memory remove requires a memory ID", nil)
	}
	if !yes {
		return memoryInput{}, domain.NewError(
			domain.Confirmation, "confirmation_required", "memory removal requires --yes",
			map[string]any{"memory_id": input.ID},
		)
	}
	return input, nil
}

func parseMemoryIDArgument(action string, args []string) (int64, error) {
	if len(args) == 0 {
		return 0, domain.NewError(
			domain.Usage, "missing_memory_id", fmt.Sprintf("memory %s requires a memory ID", action), nil,
		)
	}
	if strings.HasPrefix(args[0], "-") {
		return 0, unknownFlag(args[0])
	}
	if len(args) > 1 {
		return 0, unexpectedArgument(args[1])
	}
	return domain.ParseMemoryID(args[0])
}

func resolveMemoryText(inline, path *string, stdin io.Reader, workingDirectory string) (string, error) {
	if inline != nil {
		return *inline, nil
	}
	if path == nil {
		return "", domain.NewError(domain.Unexpected, "internal_error", "parsed memory input has no text source", nil)
	}
	var reader io.Reader
	var file *os.File
	if *path == "-" {
		if stdin == nil {
			return "", memoryReadError(*path, fmt.Errorf("stdin is unavailable"))
		}
		reader = stdin
	} else {
		resolvedPath := *path
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Join(workingDirectory, resolvedPath)
		}
		opened, err := os.Open(resolvedPath)
		if err != nil {
			return "", memoryReadError(*path, err)
		}
		file = opened
		defer file.Close()
		reader = file
	}
	content, err := io.ReadAll(io.LimitReader(reader, domain.MaxMemoryTextBytes+1))
	if err != nil {
		return "", memoryReadError(*path, err)
	}
	text := string(content)
	if err := domain.ValidateMemoryText(text); err != nil {
		return "", err
	}
	return text, nil
}

func memoryReadError(path string, err error) error {
	return domain.WrapError(
		domain.Unexpected,
		"memory_read_failed",
		"could not read memory text",
		map[string]any{"path": path},
		err,
	)
}

type memoryData struct {
	ID            int64                `json:"id"`
	Project       string               `json:"project"`
	Text          string               `json:"text"`
	CreatedBy     domain.MemoryCreator `json:"created_by"`
	HumanApproved bool                 `json:"human_approved"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
	ApprovedAt    *string              `json:"approved_at"`
	Rank          *float64             `json:"rank,omitempty"`
	Snippet       *string              `json:"snippet,omitempty"`
}

func newMemoryData(memory storage.Memory) memoryData {
	data := memoryData{
		ID: memory.ID, Project: memory.ProjectCode, Text: memory.Text, CreatedBy: memory.CreatedBy,
		HumanApproved: memory.ApprovedAt != nil,
		CreatedAt:     output.FormatTimestamp(memory.CreatedAt), UpdatedAt: output.FormatTimestamp(memory.UpdatedAt),
	}
	if memory.ApprovedAt != nil {
		approvedAt := output.FormatTimestamp(*memory.ApprovedAt)
		data.ApprovedAt = &approvedAt
	}
	return data
}

func newMemorySearchData(result storage.MemorySearchResult) memoryData {
	data := newMemoryData(result.Memory)
	data.Rank = &result.Rank
	data.Snippet = &result.Snippet
	return data
}

func (data memoryData) RenderHuman(writer io.Writer) error {
	approval := "unapproved"
	if data.HumanApproved {
		approval = "approved"
	}
	text := data.Text
	if data.Snippet != nil {
		text = *data.Snippet
	}
	_, err := fmt.Fprintf(writer, "%d  %s  %s  %s\n", data.ID, data.CreatedBy, approval, text)
	return err
}

type memoryListData []memoryData

func (data memoryListData) RenderHuman(writer io.Writer) error {
	if len(data) == 0 {
		_, err := io.WriteString(writer, "No memories.\n")
		return err
	}
	for _, memory := range data {
		if err := memory.RenderHuman(writer); err != nil {
			return err
		}
	}
	return nil
}
