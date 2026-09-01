package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/output"
	"pellets/internal/storage"
)

// ProjectCommand implements project inspection and atomic canonical-code
// renames with automation-safe redirect-conflict confirmation.
func ProjectCommand(manager app.ProjectManager) Command {
	return Command{
		Name:    "project",
		Summary: "List, show, or rename registered projects.",
		Usage: "pl project list\n  pl project show [CODE]\n" +
			"  pl [--project CODE] project rename NEW_CODE [--delete-conflicting-redirects --yes]",
		Parse: parseProject,
		NeedsCurrentWorkspace: func(globals GlobalOptions, value any) bool {
			input := value.(projectInput)
			return (input.Action == "show" || input.Action == "rename") && input.Code == "" && globals.Project == ""
		},
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
			case "rename":
				if input.DeleteConflictingRedirects != input.Yes {
					return domain.NewError(
						domain.Usage,
						"project_rename_confirmation_flags_required",
						"--delete-conflicting-redirects and --yes must be supplied together",
						map[string]any{"flags": []string{"--delete-conflicting-redirects", "--yes"}},
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
			case "rename":
				return runProjectRename(ctx, manager, invocation, input, database)
			default:
				return nil, domain.NewError(domain.Unexpected, "internal_error", "unknown parsed project action", nil)
			}
		},
	}
}

type projectInput struct {
	Action                     string
	Code                       string
	NewCode                    string
	DeleteConflictingRedirects bool
	Yes                        bool
}

func parseProject(args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.NewError(domain.Usage, "missing_subcommand", "project requires list, show, or rename", map[string]any{"command": "project"})
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
	case "rename":
		remaining := args[1:]
		if len(remaining) == 0 || strings.HasPrefix(remaining[0], "-") {
			return nil, domain.NewError(
				domain.Usage, "missing_project_code", "project rename requires NEW_CODE",
				map[string]any{"argument": "NEW_CODE"},
			)
		}
		input.NewCode = remaining[0]
		if err := domain.ValidateProjectCode(input.NewCode); err != nil {
			return nil, err
		}
		seen := make(map[string]bool)
		for _, argument := range remaining[1:] {
			if !strings.HasPrefix(argument, "-") {
				return nil, unexpectedArgument(argument)
			}
			name, _, hasValue := splitOption(argument)
			if seen[name] {
				return nil, duplicateCommandFlag(name)
			}
			seen[name] = true
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			switch name {
			case "--delete-conflicting-redirects":
				input.DeleteConflictingRedirects = true
			case "--yes":
				input.Yes = true
			default:
				return nil, unknownFlag(name)
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
	Code                 string                `json:"code"`
	GitCommonDir         string                `json:"git_common_dir"`
	GitCommonDirRelative bool                  `json:"git_common_dir_relative"`
	Workspaces           []workspaceData       `json:"workspaces"`
	Redirects            []projectRedirectData `json:"redirects"`
	CreatedAt            string                `json:"created_at"`
	UpdatedAt            string                `json:"updated_at"`
}

type projectRedirectData struct {
	Code      string `json:"code"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
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
			CreatedAt: output.FormatTimestamp(workspace.CreatedAt),
			UpdatedAt: output.FormatTimestamp(workspace.UpdatedAt),
		}
	}
	redirects := make([]projectRedirectData, len(project.Redirects))
	for index, redirect := range project.Redirects {
		redirects[index] = projectRedirectData{
			Code:      redirect.Code,
			CreatedAt: output.FormatTimestamp(redirect.CreatedAt),
			UpdatedAt: output.FormatTimestamp(redirect.UpdatedAt),
		}
	}
	return projectData{
		Code:                 project.Code,
		GitCommonDir:         project.GitCommonDir.Value,
		GitCommonDirRelative: project.GitCommonDir.Relative,
		Workspaces:           workspaces,
		Redirects:            redirects,
		CreatedAt:            output.FormatTimestamp(project.CreatedAt),
		UpdatedAt:            output.FormatTimestamp(project.UpdatedAt),
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
	for _, redirect := range data.Redirects {
		if _, err := fmt.Fprintf(writer, "  redirect %s -> %s\n", redirect.Code, data.Code); err != nil {
			return err
		}
	}
	return nil
}

type projectRenameConflictData struct {
	Code            string `json:"code"`
	CanonicalTarget string `json:"canonical_target"`
}

type projectRenameData struct {
	Status           string                      `json:"status"`
	PreviousCode     string                      `json:"previous_code"`
	Project          projectData                 `json:"project"`
	RemovedRedirects []projectRenameConflictData `json:"removed_redirects"`
}

func (data projectRenameData) RenderHuman(writer io.Writer) error {
	if data.Status == "cancelled" {
		_, err := fmt.Fprintf(writer, "Project rename cancelled; %s remains canonical and no redirects changed.\n", data.Project.Code)
		return err
	}
	if data.Status == "idempotent" {
		_, err := fmt.Fprintf(writer, "%s is already the canonical project code.\n", data.Project.Code)
		return err
	}
	if _, err := fmt.Fprintf(writer, "Renamed project %s -> %s.\n", data.PreviousCode, data.Project.Code); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "Former references such as %s-N now resolve as canonical %s-N.\n", data.PreviousCode, data.Project.Code); err != nil {
		return err
	}
	for _, conflict := range data.RemovedRedirects {
		if _, err := fmt.Fprintf(writer, "Removed redirect %s -> %s.\n", conflict.Code, conflict.CanonicalTarget); err != nil {
			return err
		}
	}
	return nil
}

func runProjectRename(
	ctx context.Context,
	manager app.ProjectManager,
	invocation Invocation,
	input projectInput,
	database app.Database,
) (projectRenameData, error) {
	plan, err := manager.PlanRename(ctx, database, invocation.WorkingDirectory, invocation.Globals.Project, input.NewCode)
	if err != nil {
		return projectRenameData{}, err
	}
	interactive := invocation.Interactive && invocation.Globals.Human
	deleteConflicts := input.DeleteConflictingRedirects && input.Yes
	if len(plan.Conflicts) > 0 && !deleteConflicts {
		if !interactive {
			return projectRenameData{}, projectRenameConfirmationRequired(plan)
		}
		wizard := newSkillWizard(invocation.Stdin, invocation.Stdout)
		if err := renderProjectRenameConflicts(invocation.Stdout, plan); err != nil {
			return projectRenameData{}, err
		}
		confirmed, err := wizard.confirm(fmt.Sprintf(
			"Delete only these redirect rules and rename %s to %s? [y/N]: ",
			plan.Project.Code, plan.NewCode,
		))
		if err != nil {
			return projectRenameData{}, err
		}
		if !confirmed {
			return projectRenameData{
				Status: "cancelled", PreviousCode: plan.Project.Code,
				Project: newProjectData(plan.Project), RemovedRedirects: make([]projectRenameConflictData, 0),
			}, nil
		}
		deleteConflicts = true
	}
	result, err := manager.Rename(ctx, database, plan, deleteConflicts)
	if err != nil {
		return projectRenameData{}, err
	}
	status := "renamed"
	if !result.Changed {
		status = "idempotent"
	}
	removed := make([]projectRenameConflictData, len(result.RemovedConflicts))
	for index, conflict := range result.RemovedConflicts {
		removed[index] = projectRenameConflictData{Code: conflict.Code, CanonicalTarget: conflict.CanonicalCode}
	}
	return projectRenameData{
		Status: status, PreviousCode: result.PreviousCode,
		Project: newProjectData(result.Project), RemovedRedirects: removed,
	}, nil
}

func renderProjectRenameConflicts(writer io.Writer, plan storage.ProjectRenamePlan) error {
	if _, err := io.WriteString(writer, "Conflicting project-code redirect rules:\n"); err != nil {
		return err
	}
	for _, conflict := range plan.Conflicts {
		if _, err := fmt.Fprintf(writer, "  %s -> %s\n", conflict.Code, conflict.CanonicalCode); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "Warning: deleting these rules can break old pellet references or reinterpret them as references to the renamed project.\n")
	return err
}

func projectRenameConfirmationRequired(plan storage.ProjectRenamePlan) error {
	conflicts := make([]map[string]any, len(plan.Conflicts))
	for index, conflict := range plan.Conflicts {
		conflicts[index] = map[string]any{
			"code":             conflict.Code,
			"project_id":       conflict.ProjectID,
			"canonical_target": conflict.CanonicalCode,
		}
	}
	retry := []string{"pl", "--project", plan.Project.Code, "project", "rename", plan.NewCode, "--delete-conflicting-redirects", "--yes"}
	return domain.NewError(
		domain.Confirmation,
		"project_rename_confirmation_required",
		"renaming requires explicit confirmation before conflicting redirect rules can be deleted",
		map[string]any{
			"project":    plan.Project.Code,
			"new_code":   plan.NewCode,
			"conflicts":  conflicts,
			"retry_argv": retry,
			"warning":    "deleting redirects can break or reinterpret old pellet references",
		},
	)
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
