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

func AddCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "add",
		Summary: "Add a pellet to the current project's queue.",
		Usage: "pl add TITLE [--description TEXT | --description-file PATH] [--external-id ID] [--group GROUP] " +
			"[--before PELLET | --after PELLET] [--maybe-later]",
		Parse: parseAdd,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(addInput)
			description, err := resolveDescription(input.Description, input.DescriptionFile, invocation.Stdin, invocation.WorkingDirectory)
			if err != nil {
				return nil, err
			}
			pellet, err := manager.Add(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				storage.NewPellet{
					Title: input.Title, Description: description,
					ExternalID: input.ExternalID, Group: input.Group,
					Status: input.Status, Placement: input.Placement,
				},
			)
			if err != nil {
				return nil, err
			}
			return newPelletData(pellet), nil
		},
	}
}

func MoveCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "move",
		Summary: "Move an active pellet relative to another active pellet.",
		Usage:   "pl move PELLET (--before OTHER | --after OTHER)",
		Parse:   parseMove,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(moveInput)
			pellet, err := manager.Move(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				input.Reference, input.Placement,
			)
			if err != nil {
				return nil, err
			}
			return newPelletData(pellet), nil
		},
	}
}

func ListCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "list",
		Summary: "List pellets in deterministic queue order.",
		Usage:   "pl list [--status STATUS] [--external-id ID] [--group GROUP] [--all] [--limit N]",
		Parse:   parseList,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(listInput)
			pellets, err := manager.List(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				storage.PelletListOptions{
					Status: input.Status, ExternalID: input.ExternalID, Group: input.Group,
					All: input.All, Limit: input.Limit,
				},
			)
			if err != nil {
				return nil, err
			}
			result := make(pelletListData, len(pellets))
			for index, pellet := range pellets {
				result[index] = newPelletData(pellet)
			}
			return result, nil
		},
	}
}

func ShowCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "show",
		Summary: "Show one complete pellet record.",
		Usage:   "pl show PELLET",
		Parse:   parseShow,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			reference := invocation.Input.(referenceInput).Reference
			pellet, err := manager.Show(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project, reference,
			)
			if err != nil {
				return nil, err
			}
			return newPelletData(pellet), nil
		},
	}
}

func EditCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "edit",
		Summary: "Edit user-controlled pellet fields.",
		Usage: "pl edit PELLET [--title TEXT] [--description TEXT | --description-file PATH] " +
			"[--external-id ID | --clear-external-id] [--group GROUP | --clear-group]",
		Parse: parseEdit,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(editInput)
			description, err := resolveOptionalDescription(input.Description, input.DescriptionFile, invocation.Stdin, invocation.WorkingDirectory)
			if err != nil {
				return nil, err
			}
			changes := storage.PelletChanges{
				Title: input.Title, Description: description,
				ExternalID: input.ExternalID, Group: input.Group,
			}
			pellet, err := manager.Edit(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				input.Reference, changes,
			)
			if err != nil {
				return nil, err
			}
			return newPelletData(pellet), nil
		},
	}
}

func NextCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "next",
		Summary: "Read the current workspace's next work without reserving it.",
		Usage:   "pl next [--external-id ID] [--group GROUP]",
		Parse:   parseNext,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(nextInput)
			selection, err := manager.Next(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				input.ExternalID, input.Group,
			)
			if err != nil {
				return nil, err
			}
			result := nextData{SelectionReason: selection.Reason}
			if selection.Pellet != nil {
				pellet := newPelletData(*selection.Pellet)
				result.Pellet = &pellet
			}
			return result, nil
		},
	}
}

func StartCommand(manager app.PelletManager) Command {
	return pelletLifecycleCommand(manager, storage.PelletStart, "Start an open pellet in the current workspace.", false)
}

func StartNextCommand(manager app.PelletManager) Command {
	return Command{
		Name:    "start-next",
		Summary: "Atomically resume or start the current workspace's next work.",
		Usage:   "pl start-next [--external-id ID] [--group GROUP]",
		Parse:   parseNext,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(nextInput)
			selection, err := manager.StartNext(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				input.ExternalID, input.Group,
			)
			if err != nil {
				return nil, err
			}
			result := nextData{SelectionReason: selection.Reason}
			if selection.Pellet != nil {
				pellet := newPelletData(*selection.Pellet)
				result.Pellet = &pellet
			}
			return result, nil
		},
	}
}

func ReleaseCommand(manager app.PelletManager) Command {
	return pelletLifecycleCommand(manager, storage.PelletRelease, "Return the current workspace's pellet to the open queue.", true)
}

func CloseCommand(manager app.PelletManager) Command {
	return pelletLifecycleCommand(manager, storage.PelletClose, "Close a pellet and remove it from the active queue.", true)
}

func ReopenCommand(manager app.PelletManager) Command {
	return pelletLifecycleCommand(manager, storage.PelletReopen, "Reopen and append a closed or deferred pellet.", false)
}

func DeferCommand(manager app.PelletManager) Command {
	return pelletLifecycleCommand(manager, storage.PelletDefer, "Defer a pellet until it is reopened.", true)
}

func pelletLifecycleCommand(manager app.PelletManager, operation storage.PelletLifecycleOperation, summary string, recovery bool) Command {
	usage := fmt.Sprintf("pl %s PELLET", operation)
	parse := func(args []string) (any, error) { return parseLifecycleReference(operation, args) }
	if recovery {
		usage += " [--recover-workspace WORKSPACE_ID --yes]"
		parse = func(args []string) (any, error) { return parseRecoverableLifecycle(operation, args) }
	}
	return Command{
		Name: string(operation), Summary: summary, Usage: usage, Parse: parse,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(lifecycleInput)
			result, err := manager.Transition(
				ctx, invocationDatabase(invocation), invocation.WorkingDirectory, invocation.Globals.Project,
				input.Reference,
				storage.PelletLifecycleRequest{Operation: operation, RecoveryWorkspaceID: input.RecoveryWorkspaceID},
			)
			if err != nil {
				return nil, err
			}
			return newLifecycleData(result), nil
		},
	}
}

type addInput struct {
	Title           string
	Description     *string
	DescriptionFile *string
	ExternalID      *string
	Group           *string
	Status          domain.PelletStatus
	Placement       *storage.PelletPlacement
}

func parseAdd(args []string) (any, error) {
	var input addInput
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			if input.Title != "" {
				return nil, unexpectedArgument(argument)
			}
			input.Title = argument
			args = args[1:]
			continue
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--description", "--description-file", "--external-id", "--group", "--before", "--after":
			var err error
			value, args, err = takeCommandFlagValue(
				args, name, value, hasValue,
				name == "--description-file", name == "--description",
			)
			if err != nil {
				return nil, err
			}
			switch name {
			case "--description":
				input.Description = stringPointer(value)
			case "--description-file":
				input.DescriptionFile = stringPointer(value)
			case "--external-id":
				input.ExternalID = stringPointer(value)
			case "--group":
				input.Group = stringPointer(value)
			case "--before", "--after":
				reference, parseErr := domain.ParsePelletReference(value)
				if parseErr != nil {
					return nil, parseErr
				}
				input.Placement = &storage.PelletPlacement{Target: reference, Before: name == "--before"}
			}
		case "--maybe-later":
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			input.Status = domain.PelletMaybeLater
			args = args[1:]
		default:
			return nil, unknownFlag(name)
		}
	}
	if input.Title == "" || strings.TrimSpace(input.Title) == "" {
		return nil, domain.NewError(domain.Usage, "missing_title", "add requires a non-empty TITLE", nil)
	}
	if input.Description != nil && input.DescriptionFile != nil {
		return nil, conflictingFlags("--description", "--description-file")
	}
	if seen["--before"] && seen["--after"] {
		return nil, conflictingFlags("--before", "--after")
	}
	if input.Status == domain.PelletMaybeLater && input.Placement != nil {
		return nil, conflictingFlags("--maybe-later", placementFlagName(*input.Placement))
	}
	return input, nil
}

type moveInput struct {
	Reference domain.PelletReference
	Placement storage.PelletPlacement
}

func parseMove(args []string) (any, error) {
	var input moveInput
	referenceSet := false
	placementSet := false
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			if referenceSet {
				return nil, unexpectedArgument(argument)
			}
			reference, err := domain.ParsePelletReference(argument)
			if err != nil {
				return nil, err
			}
			input.Reference = reference
			referenceSet = true
			args = args[1:]
			continue
		}

		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		if name != "--before" && name != "--after" {
			return nil, unknownFlag(name)
		}
		var err error
		value, args, err = takeCommandFlagValue(args, name, value, hasValue, false, false)
		if err != nil {
			return nil, err
		}
		target, err := domain.ParsePelletReference(value)
		if err != nil {
			return nil, err
		}
		input.Placement = storage.PelletPlacement{Target: target, Before: name == "--before"}
		placementSet = true
	}
	if !referenceSet {
		return nil, domain.NewError(domain.Usage, "missing_reference", "move requires a pellet reference", nil)
	}
	if seen["--before"] && seen["--after"] {
		return nil, conflictingFlags("--before", "--after")
	}
	if !placementSet {
		return nil, domain.NewError(domain.Usage, "missing_placement", "move requires --before or --after", nil)
	}
	if input.Reference == input.Placement.Target {
		return nil, domain.NewError(
			domain.Usage, "invalid_move_target", "a pellet cannot be moved relative to itself",
			map[string]any{"pellet_id": input.Reference.String()},
		)
	}
	return input, nil
}

type listInput struct {
	Status     *domain.PelletStatus
	ExternalID *string
	Group      *string
	All        bool
	Limit      *int64
}

func parseList(args []string) (any, error) {
	var input listInput
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			return nil, unexpectedArgument(argument)
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--status", "--external-id", "--group", "--limit":
			var err error
			value, args, err = takeCommandFlagValue(args, name, value, hasValue, false, false)
			if err != nil {
				return nil, err
			}
			switch name {
			case "--status":
				status := domain.PelletStatus(value)
				if err := domain.ValidatePelletStatus(status); err != nil {
					return nil, err
				}
				input.Status = &status
			case "--external-id":
				input.ExternalID = stringPointer(value)
			case "--group":
				input.Group = stringPointer(value)
			case "--limit":
				limit, parseErr := strconv.ParseInt(value, 10, 64)
				if parseErr != nil || limit <= 0 {
					return nil, invalidLimit(value)
				}
				input.Limit = &limit
			}
		case "--all":
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			input.All = true
			args = args[1:]
		default:
			return nil, unknownFlag(name)
		}
	}
	if input.Status != nil && input.All {
		return nil, conflictingFlags("--status", "--all")
	}
	return input, nil
}

type referenceInput struct{ Reference domain.PelletReference }

func parseShow(args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.NewError(domain.Usage, "missing_reference", "show requires a pellet reference", nil)
	}
	if strings.HasPrefix(args[0], "-") {
		return nil, unknownFlag(args[0])
	}
	if len(args) > 1 {
		return nil, unexpectedArgument(args[1])
	}
	reference, err := domain.ParsePelletReference(args[0])
	if err != nil {
		return nil, err
	}
	return referenceInput{Reference: reference}, nil
}

type editInput struct {
	Reference       domain.PelletReference
	Title           *string
	Description     *string
	DescriptionFile *string
	ExternalID      storage.NullableTextChange
	Group           storage.NullableTextChange
}

func parseEdit(args []string) (any, error) {
	var input editInput
	referenceSet := false
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			if referenceSet {
				return nil, unexpectedArgument(argument)
			}
			reference, err := domain.ParsePelletReference(argument)
			if err != nil {
				return nil, err
			}
			input.Reference = reference
			referenceSet = true
			args = args[1:]
			continue
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--title", "--description", "--description-file", "--external-id", "--group":
			var err error
			value, args, err = takeCommandFlagValue(
				args, name, value, hasValue,
				name == "--description-file", name == "--description" || name == "--title",
			)
			if err != nil {
				return nil, err
			}
			switch name {
			case "--title":
				input.Title = stringPointer(value)
			case "--description":
				input.Description = stringPointer(value)
			case "--description-file":
				input.DescriptionFile = stringPointer(value)
			case "--external-id":
				input.ExternalID = storage.NullableTextChange{Set: true, Value: stringPointer(value)}
			case "--group":
				input.Group = storage.NullableTextChange{Set: true, Value: stringPointer(value)}
			}
		case "--clear-external-id", "--clear-group":
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			if name == "--clear-external-id" {
				input.ExternalID = storage.NullableTextChange{Set: true}
			} else {
				input.Group = storage.NullableTextChange{Set: true}
			}
			args = args[1:]
		default:
			return nil, unknownFlag(name)
		}
	}
	if !referenceSet {
		return nil, domain.NewError(domain.Usage, "missing_reference", "edit requires a pellet reference", nil)
	}
	if input.Description != nil && input.DescriptionFile != nil {
		return nil, conflictingFlags("--description", "--description-file")
	}
	if seen["--external-id"] && seen["--clear-external-id"] {
		return nil, conflictingFlags("--external-id", "--clear-external-id")
	}
	if seen["--group"] && seen["--clear-group"] {
		return nil, conflictingFlags("--group", "--clear-group")
	}
	if input.Title == nil && input.Description == nil && input.DescriptionFile == nil && !input.ExternalID.Set && !input.Group.Set {
		return nil, domain.NewError(domain.Usage, "missing_edit", "at least one editable pellet field is required", nil)
	}
	if input.Title != nil && strings.TrimSpace(*input.Title) == "" {
		return nil, domain.NewError(domain.Usage, "invalid_pellet_field", "pellet title must not be empty", map[string]any{"field": "title"})
	}
	return input, nil
}

type nextInput struct {
	ExternalID *string
	Group      *string
}

type lifecycleInput struct {
	Reference           domain.PelletReference
	RecoveryWorkspaceID *int64
}

func parseLifecycleReference(operation storage.PelletLifecycleOperation, args []string) (any, error) {
	if len(args) == 0 {
		return nil, missingLifecycleReference(operation)
	}
	if strings.HasPrefix(args[0], "-") {
		return nil, unknownFlag(args[0])
	}
	if len(args) > 1 {
		return nil, unexpectedArgument(args[1])
	}
	reference, err := domain.ParsePelletReference(args[0])
	if err != nil {
		return nil, err
	}
	return lifecycleInput{Reference: reference}, nil
}

func parseRecoverableLifecycle(operation storage.PelletLifecycleOperation, args []string) (any, error) {
	var input lifecycleInput
	referenceSet := false
	yes := false
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			if referenceSet {
				return nil, unexpectedArgument(argument)
			}
			reference, err := domain.ParsePelletReference(argument)
			if err != nil {
				return nil, err
			}
			input.Reference = reference
			referenceSet = true
			args = args[1:]
			continue
		}

		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--recover-workspace":
			var err error
			value, args, err = takeCommandFlagValue(args, name, value, hasValue, false, false)
			if err != nil {
				return nil, err
			}
			workspaceID, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || workspaceID <= 0 || strconv.FormatInt(workspaceID, 10) != value {
				return nil, domain.NewError(
					domain.Usage, "invalid_workspace_id", "workspace ID must be a positive canonical integer",
					map[string]any{"workspace_id": value},
				)
			}
			input.RecoveryWorkspaceID = &workspaceID
		case "--yes":
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			yes = true
			args = args[1:]
		default:
			return nil, unknownFlag(name)
		}
	}
	if !referenceSet {
		return nil, missingLifecycleReference(operation)
	}
	if input.RecoveryWorkspaceID == nil && yes {
		return nil, domain.NewError(
			domain.Usage, "recovery_workspace_required", "--yes requires --recover-workspace for lifecycle recovery",
			map[string]any{"flag": "--yes"},
		)
	}
	if input.RecoveryWorkspaceID != nil && !yes {
		return nil, domain.NewError(
			domain.Confirmation, "confirmation_required", "workspace recovery requires --yes",
			map[string]any{"workspace_id": *input.RecoveryWorkspaceID},
		)
	}
	return input, nil
}

func missingLifecycleReference(operation storage.PelletLifecycleOperation) error {
	return domain.NewError(
		domain.Usage,
		"missing_reference",
		fmt.Sprintf("%s requires a pellet reference", operation),
		nil,
	)
}

func parseNext(args []string) (any, error) {
	var input nextInput
	seen := make(map[string]bool)
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			return nil, unexpectedArgument(argument)
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		if name != "--external-id" && name != "--group" {
			return nil, unknownFlag(name)
		}
		var err error
		value, args, err = takeCommandFlagValue(args, name, value, hasValue, false, false)
		if err != nil {
			return nil, err
		}
		if name == "--external-id" {
			input.ExternalID = stringPointer(value)
		} else {
			input.Group = stringPointer(value)
		}
	}
	return input, nil
}

func takeCommandFlagValue(args []string, name, value string, hasValue, allowDash, allowEmpty bool) (string, []string, error) {
	if hasValue {
		if value == "" && !allowEmpty {
			return "", nil, missingFlagValue(name)
		}
		return value, args[1:], nil
	}
	if len(args) < 2 || strings.HasPrefix(args[1], "-") && !(allowDash && args[1] == "-") {
		return "", nil, missingFlagValue(name)
	}
	if args[1] == "" && !allowEmpty {
		return "", nil, missingFlagValue(name)
	}
	return args[1], args[2:], nil
}

func resolveDescription(inline, path *string, stdin io.Reader, workingDirectory string) (string, error) {
	description, err := resolveOptionalDescription(inline, path, stdin, workingDirectory)
	if err != nil || description == nil {
		return "", err
	}
	return *description, nil
}

func resolveOptionalDescription(inline, path *string, stdin io.Reader, workingDirectory string) (*string, error) {
	if inline != nil {
		return inline, nil
	}
	if path == nil {
		return nil, nil
	}
	var content []byte
	var err error
	if *path == "-" {
		if stdin == nil {
			err = fmt.Errorf("stdin is unavailable")
		} else {
			content, err = io.ReadAll(stdin)
		}
	} else {
		resolvedPath := *path
		if !filepath.IsAbs(resolvedPath) {
			resolvedPath = filepath.Join(workingDirectory, resolvedPath)
		}
		content, err = os.ReadFile(resolvedPath)
	}
	if err != nil {
		return nil, domain.WrapError(
			domain.Unexpected,
			"description_read_failed",
			"could not read the pellet description",
			map[string]any{"path": *path},
			err,
		)
	}
	value := string(content)
	return &value, nil
}

func invocationDatabase(invocation Invocation) app.Database {
	return app.Database{Root: invocation.Database.Root, Path: invocation.Database.Path}
}

func stringPointer(value string) *string { return &value }

func placementFlagName(placement storage.PelletPlacement) string {
	if placement.Before {
		return "--before"
	}
	return "--after"
}

func invalidLimit(value string) error {
	return domain.NewError(domain.Usage, "invalid_limit", "limit must be a positive integer", map[string]any{"limit": value})
}

type pelletData struct {
	ID          string               `json:"id"`
	Project     string               `json:"project"`
	Number      int64                `json:"number"`
	Title       string               `json:"title"`
	Description string               `json:"description"`
	ExternalID  *string              `json:"external_id"`
	Group       *string              `json:"group"`
	Status      domain.PelletStatus  `json:"status"`
	Priority    *int64               `json:"priority"`
	Workspace   *pelletWorkspaceData `json:"workspace"`
	CreatedAt   string               `json:"created_at"`
	UpdatedAt   string               `json:"updated_at"`
	CompletedAt *string              `json:"completed_at"`
}

type pelletWorkspaceData struct {
	ID               int64  `json:"id"`
	RootPath         string `json:"root_path"`
	RootPathRelative bool   `json:"root_path_relative"`
	GitDir           string `json:"git_dir"`
	GitDirRelative   bool   `json:"git_dir_relative"`
}

func newPelletData(pellet storage.Pellet) pelletData {
	data := pelletData{
		ID: pellet.Reference.String(), Project: pellet.Reference.ProjectCode, Number: pellet.Reference.Number,
		Title: pellet.Title, Description: pellet.Description,
		ExternalID: pellet.ExternalID, Group: pellet.Group, Status: pellet.Status, Priority: pellet.Priority,
		CreatedAt: output.FormatTimestamp(pellet.CreatedAt), UpdatedAt: output.FormatTimestamp(pellet.UpdatedAt),
	}
	if pellet.Workspace != nil {
		data.Workspace = &pelletWorkspaceData{
			ID: pellet.Workspace.ID, RootPath: pellet.Workspace.RootPath.Value,
			RootPathRelative: pellet.Workspace.RootPath.Relative,
			GitDir:           pellet.Workspace.GitDir.Value, GitDirRelative: pellet.Workspace.GitDir.Relative,
		}
	}
	if pellet.CompletedAt != nil {
		formatted := output.FormatTimestamp(*pellet.CompletedAt)
		data.CompletedAt = &formatted
	}
	return data
}

type lifecycleData struct {
	pelletData
	RecoveredWorkspace *pelletWorkspaceData `json:"recovered_workspace,omitempty"`
}

func newLifecycleData(result storage.PelletLifecycleResult) lifecycleData {
	data := lifecycleData{pelletData: newPelletData(result.Pellet)}
	if result.RecoveredWorkspace != nil {
		data.RecoveredWorkspace = newPelletWorkspaceData(*result.RecoveredWorkspace)
	}
	return data
}

func (data lifecycleData) RenderHuman(writer io.Writer) error {
	if err := renderPelletSummary(writer, data.pelletData); err != nil {
		return err
	}
	if data.RecoveredWorkspace != nil {
		_, err := fmt.Fprintf(writer, "Recovered workspace %d.\n", data.RecoveredWorkspace.ID)
		return err
	}
	return nil
}

func newPelletWorkspaceData(workspace storage.Workspace) *pelletWorkspaceData {
	return &pelletWorkspaceData{
		ID: workspace.ID, RootPath: workspace.RootPath.Value,
		RootPathRelative: workspace.RootPath.Relative,
		GitDir:           workspace.GitDir.Value, GitDirRelative: workspace.GitDir.Relative,
	}
}

func (data pelletData) RenderHuman(writer io.Writer) error {
	return renderPelletSummary(writer, data)
}

type pelletListData []pelletData

func (data pelletListData) RenderHuman(writer io.Writer) error {
	if len(data) == 0 {
		_, err := io.WriteString(writer, "No pellets.\n")
		return err
	}
	for _, pellet := range data {
		if err := renderPelletSummary(writer, pellet); err != nil {
			return err
		}
	}
	return nil
}

type nextData struct {
	SelectionReason storage.NextSelectionReason `json:"selection_reason"`
	Pellet          *pelletData                 `json:"pellet"`
}

func (data nextData) RenderHuman(writer io.Writer) error {
	if data.Pellet == nil {
		_, err := io.WriteString(writer, "No open pellets.\n")
		return err
	}
	return renderPelletSummary(writer, *data.Pellet)
}

func renderPelletSummary(writer io.Writer, pellet pelletData) error {
	priority := "p=-"
	if pellet.Priority != nil {
		priority = fmt.Sprintf("p=%d", *pellet.Priority)
	}
	owner := ""
	if pellet.Workspace != nil {
		owner = fmt.Sprintf("  workspace=%d", pellet.Workspace.ID)
	}
	_, err := fmt.Fprintf(writer, "%s  %s  %s%s  %s\n", pellet.ID, pellet.Status, priority, owner, pellet.Title)
	return err
}
