package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

// InitCommand registers the current Git worktree as a workspace of one
// logical project.
func InitCommand(manager app.ProjectManager) Command {
	return Command{
		Name:                  "init",
		Summary:               "Register the current Git repository and worktree.",
		Usage:                 "pl init --code CODE",
		SkipDatabaseDiscovery: true,
		Validate: func(globals GlobalOptions, _ any) error {
			if globals.Project != "" {
				return projectNotAllowed("init")
			}
			return nil
		},
		Parse: parseInit,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(initInput)
			project, err := manager.Init(ctx, invocation.WorkingDirectory, input.Code)
			if err != nil {
				return nil, err
			}
			return newProjectData(project), nil
		},
	}
}

type initInput struct {
	Code string
}

func parseInit(args []string) (any, error) {
	var input initInput
	seenCode := false
	for len(args) > 0 {
		argument := args[0]
		if !strings.HasPrefix(argument, "-") {
			return nil, unexpectedArgument(argument)
		}
		name, value, hasValue := splitOption(argument)
		if name != "--code" {
			return nil, unknownFlag(name)
		}
		if seenCode {
			return nil, duplicateCommandFlag(name)
		}
		seenCode = true
		if !hasValue {
			if len(args) < 2 || strings.HasPrefix(args[1], "-") {
				return nil, missingFlagValue(name)
			}
			value = args[1]
			args = args[1:]
		}
		if value == "" {
			return nil, missingFlagValue(name)
		}
		input.Code = value
		args = args[1:]
	}
	if !seenCode {
		return nil, domain.NewError(
			domain.Usage,
			"missing_required_flag",
			"flag \"--code\" is required",
			map[string]any{"flag": "--code"},
		)
	}
	if err := domain.ValidateProjectCode(input.Code); err != nil {
		return nil, err
	}
	return input, nil
}

// ProjectCommand implements the database-level project list and project show
// command family.
func ProjectCommand(manager app.ProjectManager) Command {
	return Command{
		Name:    "project",
		Summary: "List or show registered projects.",
		Usage:   "pl project list\n  pl project show [CODE]",
		Parse:   parseProject,
		Validate: func(globals GlobalOptions, value any) error {
			input := value.(projectInput)
			switch input.Action {
			case "list":
				if globals.Project != "" {
					return projectNotAllowed("project list")
				}
			case "show":
				if input.Code != "" && globals.Project != "" {
					return domain.NewError(
						domain.Usage,
						"conflicting_project_selection",
						"project show accepts either positional CODE or --project, not both",
						map[string]any{"code": input.Code, "project": globals.Project},
					)
				}
			}
			return nil
		},
		ResultName: func(value any) string {
			input := value.(projectInput)
			return "project " + input.Action
		},
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			input := invocation.Input.(projectInput)
			database := app.Database{Root: invocation.Database.Root, Path: invocation.Database.Path}
			switch input.Action {
			case "list":
				projects, err := manager.List(ctx, database)
				if err != nil {
					return nil, err
				}
				result := make(projectListData, len(projects))
				for index, project := range projects {
					result[index] = newProjectData(project)
				}
				return result, nil
			case "show":
				code := input.Code
				if code == "" {
					code = invocation.Globals.Project
				}
				var project storage.Project
				var err error
				if code == "" {
					project, err = manager.ShowCurrent(ctx, database, invocation.WorkingDirectory)
				} else {
					project, err = manager.ShowByCode(ctx, database, code)
				}
				if err != nil {
					return nil, err
				}
				return newProjectData(project), nil
			default:
				return nil, domain.NewError(domain.Unexpected, "internal_error", "unknown parsed project action", nil)
			}
		},
	}
}

type projectInput struct {
	Action string
	Code   string
}

func parseProject(args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.NewError(domain.Usage, "missing_subcommand", "project requires list or show", map[string]any{"command": "project"})
	}
	action := args[0]
	if strings.HasPrefix(action, "-") {
		return nil, unknownFlag(action)
	}
	input := projectInput{Action: action}
	switch action {
	case "list":
		if _, err := ParseNoArguments(args[1:]); err != nil {
			return nil, err
		}
	case "show":
		remaining := args[1:]
		if len(remaining) > 0 && strings.HasPrefix(remaining[0], "-") {
			return nil, unknownFlag(remaining[0])
		}
		if len(remaining) > 1 {
			return nil, unexpectedArgument(remaining[1])
		}
		if len(remaining) == 1 {
			input.Code = remaining[0]
			if err := domain.ValidateProjectCode(input.Code); err != nil {
				return nil, err
			}
		}
	default:
		return nil, domain.NewError(
			domain.Usage,
			"unknown_subcommand",
			fmt.Sprintf("unknown project subcommand %q", action),
			map[string]any{"command": "project", "subcommand": action},
		)
	}
	return input, nil
}

type projectData struct {
	Code                 string          `json:"code"`
	GitCommonDir         string          `json:"git_common_dir"`
	GitCommonDirRelative bool            `json:"git_common_dir_relative"`
	Workspaces           []workspaceData `json:"workspaces"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

type workspaceData struct {
	ID               int64  `json:"id"`
	RootPath         string `json:"root_path"`
	RootPathRelative bool   `json:"root_path_relative"`
	GitDir           string `json:"git_dir"`
	GitDirRelative   bool   `json:"git_dir_relative"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

func newProjectData(project storage.Project) projectData {
	workspaces := make([]workspaceData, len(project.Workspaces))
	for index, workspace := range project.Workspaces {
		workspaces[index] = workspaceData{
			ID: workspace.ID, RootPath: workspace.RootPath.Value,
			RootPathRelative: workspace.RootPath.Relative,
			GitDir:           workspace.GitDir.Value, GitDirRelative: workspace.GitDir.Relative,
			CreatedAt: workspace.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: workspace.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return projectData{
		Code:                 project.Code,
		GitCommonDir:         project.GitCommonDir.Value,
		GitCommonDirRelative: project.GitCommonDir.Relative,
		Workspaces:           workspaces,
		CreatedAt:            project.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:            project.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (data projectData) RenderHuman(writer io.Writer) error {
	if _, err := fmt.Fprintf(writer, "%s  repository=%s\n", data.Code, data.GitCommonDir); err != nil {
		return err
	}
	for _, workspace := range data.Workspaces {
		if _, err := fmt.Fprintf(writer, "  workspace %d  %s  git-dir=%s\n", workspace.ID, workspace.RootPath, workspace.GitDir); err != nil {
			return err
		}
	}
	return nil
}

type projectListData []projectData

func (data projectListData) RenderHuman(writer io.Writer) error {
	for _, project := range data {
		if err := project.RenderHuman(writer); err != nil {
			return err
		}
	}
	return nil
}

func projectNotAllowed(command string) error {
	return domain.NewError(
		domain.Usage,
		"project_not_allowed",
		fmt.Sprintf("--project is not valid for %s", command),
		map[string]any{"command": command},
	)
}

func duplicateCommandFlag(flag string) error {
	return domain.NewError(
		domain.Usage,
		"duplicate_flag",
		fmt.Sprintf("flag %q may only be specified once", flag),
		map[string]any{"flag": flag},
	)
}
