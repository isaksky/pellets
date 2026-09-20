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

const groupUsage = `pl [--project CODE] group create NAME [--context TEXT | --context-file PATH]
  pl [--project CODE] group list
  pl [--project CODE] group show GROUP_ID
  pl [--project CODE] group edit GROUP_ID (--context TEXT | --context-file PATH | --clear-context) [--revision N]
  pl [--project CODE] group rename GROUP_ID NEW_NAME [--revision N]

Groups belong to the current logical project, or global --project CODE.
GROUP_ID is the stable positive integer from group list/create/show.
Names are exact, case-sensitive UTF-8. Duplicate names are rejected, never merged.
Create without context makes an empty group, with no pellets required.
Context is literal UTF-8 Markdown (at most 1 MiB); fences and newlines are preserved.
--context-file - explicitly reads stdin to EOF; no group command prompts.
--context '' or an empty file clears context; an omitted edit is an error.
--revision N checks the revision previously read; stale writes fail without changes.
Omitting --revision uses the current revision, still checking for concurrent writes.
Use --json or --pretty before group for stable, non-interactive machine output.
Group lists omit context; show includes the complete context document.

Examples (POSIX shell; replace 7 with the returned group ID):
  pl group create parser-rollout
  pl group create design --context-file './group context.md'
  pl --json --project foo group show 7
  pl --json --project foo group edit 7 --context-file context.md --revision 1
  pl group edit 7 --context ''
  pl group rename 7 parser-v2 --revision 3
pl group edit 7 --context-file - <<'MARKDOWN'
# Shared context
` + "```mermaid\nflowchart LR\n  A --> B\n```" + `
MARKDOWN

For multiline text, prefer a file or a quoted heredoc delimiter. Keep the closing
MARKDOWN at column one. Use --context='- item' for text starting with a dash.
Do not double-quote Markdown containing shell backticks or $ substitutions.
Group management is independent
of selecting or inspecting a pellet; no extra group lookup is required.`

// GroupCommand manages shared project context independently of queue members.
func GroupCommand(manager app.GroupManager) Command {
	return Command{
		Name: "group", Summary: "Create, list, show, edit, or rename shared group context.",
		Usage:                 groupUsage,
		Subcommands:           []string{"create", "list", "show", "edit", "rename"},
		Parse:                 parseGroup,
		NeedsCurrentWorkspace: func(globals GlobalOptions, _ any) bool { return globals.Project == "" },
		ResultName:            func(value any) string { return "group " + value.(groupInput).Action },
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(groupInput)
			db, cwd, project := invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project
			if input.Action == "list" {
				groups, err := manager.List(ctx, db, cwd, project)
				if err != nil {
					return nil, err
				}
				result := make(groupListData, len(groups))
				for i, group := range groups {
					result[i] = newGroupSummary(group)
				}
				return result, nil
			}
			markdown, err := resolveGroupContext(input, invocation)
			if err != nil {
				return nil, err
			}
			var group storage.Group
			if input.Action == "create" {
				group, err = manager.CreateWithContext(ctx, db, cwd, project, input.Name, markdown)
			} else if input.Action == "show" {
				group, err = manager.Read(ctx, db, cwd, project, input.ID)
			} else {
				revision := input.Revision
				if revision == 0 {
					current, readErr := manager.Read(ctx, db, cwd, project, input.ID)
					if readErr != nil {
						return nil, readErr
					}
					revision = current.Revision
				}
				if input.Action == "edit" {
					group, err = manager.EditContext(ctx, db, cwd, project, input.ID, revision, markdown)
				} else {
					group, err = manager.Rename(ctx, db, cwd, project, input.ID, revision, input.Name)
				}
				if err != nil && domain.PublicError(err).Code == "group_revision_conflict" {
					public := domain.PublicError(err)
					reload := []string{"pl", "--json"}
					if project != "" {
						reload = append(reload, "--project", project)
					}
					reload = append(reload, "group", "show", strconv.FormatInt(input.ID, 10))
					return nil, domain.NewError(domain.Conflict, public.Code,
						"group changed; inspect it with group show, reconcile your changes, then retry with its revision",
						map[string]any{"group_id": input.ID, "revision": public.Details["revision"], "expected_revision": revision, "reload_argv": reload})
				}
			}
			if err != nil {
				return nil, err
			}
			return newGroupData(group), nil
		},
	}
}

type groupInput struct {
	Action      string
	ID          int64
	Name        string
	Revision    int64
	Context     *string
	ContextFile *string
	Clear       bool
}

func parseGroup(args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.NewError(domain.Usage, "missing_subcommand", "group requires create, list, show, edit, or rename", map[string]any{"command": "group"})
	}
	input := groupInput{Action: args[0]}
	args = args[1:]
	count := 0
	switch input.Action {
	case "list":
	case "create", "show", "edit":
		count = 1
	case "rename":
		count = 2
	default:
		if strings.HasPrefix(input.Action, "-") {
			return nil, unknownFlag(input.Action)
		}
		return nil, domain.NewError(domain.Usage, "unknown_subcommand", fmt.Sprintf("unknown group subcommand %q", input.Action), map[string]any{"command": "group", "subcommand": input.Action})
	}
	var positional []string
	seen := map[string]bool{}
	endOptions := false
	for len(args) > 0 {
		argument := args[0]
		if !endOptions && argument == "--" {
			endOptions = true
			args = args[1:]
			continue
		}
		if endOptions || !strings.HasPrefix(argument, "-") {
			if len(positional) >= count {
				return nil, unexpectedArgument(argument)
			}
			positional = append(positional, argument)
			args = args[1:]
			continue
		}
		name, value, hasValue := splitOption(argument)
		allowed := (input.Action == "create" || input.Action == "edit") && (name == "--context" || name == "--context-file") ||
			input.Action == "edit" && name == "--clear-context" ||
			(input.Action == "edit" || input.Action == "rename") && name == "--revision"
		if !allowed {
			return nil, unknownFlag(name)
		}
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		if name == "--clear-context" {
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			input.Clear = true
			args = args[1:]
			continue
		}
		var err error
		value, args, err = takeCommandFlagValue(args, name, value, hasValue, name == "--context-file", name == "--context")
		if err != nil {
			return nil, err
		}
		switch name {
		case "--context":
			input.Context = stringPointer(value)
			if err := storage.ValidateGroupContext(value); err != nil {
				return nil, err
			}
		case "--context-file":
			input.ContextFile = stringPointer(value)
		case "--revision":
			input.Revision, err = parseGroupInteger(value, "revision")
			if err != nil {
				return nil, err
			}
		}
	}
	if len(positional) != count {
		return nil, domain.NewError(domain.Usage, "missing_group_argument", "group "+input.Action+" requires "+map[string]string{"create": "NAME", "show": "GROUP_ID", "edit": "GROUP_ID", "rename": "GROUP_ID NEW_NAME"}[input.Action], nil)
	}
	if input.Action == "create" {
		input.Name = positional[0]
	} else if count > 0 {
		var err error
		input.ID, err = parseGroupInteger(positional[0], "id")
		if err != nil {
			return nil, err
		}
		if input.Action == "rename" {
			input.Name = positional[1]
		}
	}
	if input.Action == "create" || input.Action == "rename" {
		if err := storage.ValidateGroupName(input.Name); err != nil {
			return nil, err
		}
	}
	if input.Context != nil && input.ContextFile != nil {
		return nil, conflictingFlags("--context", "--context-file")
	}
	if input.Clear && input.Context != nil {
		return nil, conflictingFlags("--clear-context", "--context")
	}
	if input.Clear && input.ContextFile != nil {
		return nil, conflictingFlags("--clear-context", "--context-file")
	}
	if input.Action == "edit" && input.Context == nil && input.ContextFile == nil && !input.Clear {
		return nil, domain.NewError(domain.Usage, "missing_edit", "group edit requires --context, --context-file, or --clear-context; omitting context does not clear it", nil)
	}
	return input, nil
}

func parseGroupInteger(value, field string) (int64, error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return 0, domain.NewError(domain.Usage, "invalid_group_"+field, "group "+field+" must be a positive canonical integer", map[string]any{field: value})
	}
	return n, nil
}

func resolveGroupContext(input groupInput, invocation Invocation) (string, error) {
	if input.Context != nil {
		return *input.Context, nil
	}
	if input.ContextFile == nil {
		return "", nil
	}
	path := *input.ContextFile
	reader := invocation.Stdin
	if path != "-" {
		resolved := path
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(invocation.WorkingDirectory, resolved)
		}
		file, err := os.Open(resolved)
		if err != nil {
			return "", groupContextReadError(path, err)
		}
		defer file.Close()
		reader = file
	}
	if reader == nil {
		return "", groupContextReadError(path, fmt.Errorf("stdin is unavailable"))
	}
	content, err := io.ReadAll(io.LimitReader(reader, storage.MaxGroupContextBytes+1))
	if err != nil {
		return "", groupContextReadError(path, err)
	}
	markdown := string(content)
	return markdown, storage.ValidateGroupContext(markdown)
}

func groupContextReadError(path string, err error) error {
	return domain.WrapError(domain.Unexpected, "group_context_read_failed", "could not read group context; check --context-file and its readability", map[string]any{"path": path}, err)
}

type groupSummary struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
}

type groupData struct {
	groupSummary
	Context   string `json:"context"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func newGroupSummary(group storage.Group) groupSummary {
	return groupSummary{ID: group.ID, ProjectID: group.ProjectID, Name: group.Name, Revision: group.Revision}
}

func newGroupData(group storage.Group) groupData {
	return groupData{groupSummary: newGroupSummary(group), Context: group.Context,
		CreatedAt: output.FormatTimestamp(group.CreatedAt), UpdatedAt: output.FormatTimestamp(group.UpdatedAt)}
}

func (data groupData) RenderHuman(writer io.Writer) error {
	if _, err := fmt.Fprintf(writer, "Group %d  %q  revision=%d  project-id=%d\nContext:\n", data.ID, data.Name, data.Revision, data.ProjectID); err != nil {
		return err
	}
	if data.Context == "" {
		_, err := io.WriteString(writer, "(empty)\n")
		return err
	}
	_, err := io.WriteString(writer, data.Context)
	if err == nil && !strings.HasSuffix(data.Context, "\n") {
		_, err = io.WriteString(writer, "\n")
	}
	return err
}

// Markdown must retain its line layout even on narrow terminals.
func (groupData) PreserveHumanLayout() bool { return true }

type groupListData []groupSummary

func (data groupListData) RenderHuman(writer io.Writer) error {
	if len(data) == 0 {
		_, err := io.WriteString(writer, "No groups.\n")
		return err
	}
	for _, group := range data {
		if _, err := fmt.Fprintf(writer, "%d  %q  revision=%d\n", group.ID, group.Name, group.Revision); err != nil {
			return err
		}
	}
	return nil
}
