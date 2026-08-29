package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"pellets/internal/app"
	"pellets/internal/domain"
)

// SkillCommand implements the database-independent portable skill installer.
func SkillCommand(installer app.SkillInstaller) Command {
	return Command{
		Name:                  "skill",
		Summary:               "Install the Pellets agent skill.",
		Usage:                 "pl skill install [--scope repo|personal] [--agent codex|claude|both] [--yes] [--dry-run] [--force]",
		SkipDatabaseDiscovery: true,
		Parse:                 parseSkill,
		Validate: func(globals GlobalOptions, _ any) error {
			if globals.Project != "" {
				return projectNotAllowed("skill install")
			}
			return nil
		},
		ResultName: func(any) string { return "skill install" },
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			return runSkillInstall(ctx, installer, invocation)
		},
	}
}

type skillInput struct {
	Action string
	Scope  app.SkillScope
	Agent  app.SkillAgent
	Yes    bool
	DryRun bool
	Force  bool
}

func parseSkill(args []string) (any, error) {
	if len(args) == 0 {
		return nil, domain.NewError(
			domain.Usage, "missing_subcommand", "skill requires install",
			map[string]any{"command": "skill"},
		)
	}
	if strings.HasPrefix(args[0], "-") {
		return nil, unknownFlag(args[0])
	}
	if args[0] != "install" {
		return nil, domain.NewError(
			domain.Usage,
			"unknown_subcommand",
			fmt.Sprintf("unknown skill subcommand %q", args[0]),
			map[string]any{"command": "skill", "subcommand": args[0]},
		)
	}
	input := skillInput{Action: "install"}
	seen := make(map[string]bool)
	remaining := args[1:]
	for len(remaining) > 0 {
		argument := remaining[0]
		if !strings.HasPrefix(argument, "-") {
			return nil, unexpectedArgument(argument)
		}
		name, value, hasValue := splitOption(argument)
		if seen[name] {
			return nil, duplicateCommandFlag(name)
		}
		seen[name] = true
		switch name {
		case "--scope", "--agent":
			var err error
			value, remaining, err = takeCommandFlagValue(remaining, name, value, hasValue, false, false)
			if err != nil {
				return nil, err
			}
			if name == "--scope" {
				input.Scope = app.SkillScope(value)
				if input.Scope != app.SkillScopeRepository && input.Scope != app.SkillScopePersonal {
					return nil, domain.NewError(
						domain.Usage, "invalid_skill_scope",
						fmt.Sprintf("invalid skill scope %q; expected repo or personal", value),
						map[string]any{"scope": value, "allowed": []string{"repo", "personal"}},
					)
				}
			} else {
				input.Agent = app.SkillAgent(value)
				if input.Agent != app.SkillAgentCodex && input.Agent != app.SkillAgentClaude && input.Agent != app.SkillAgentBoth {
					return nil, domain.NewError(
						domain.Usage, "invalid_skill_agent",
						fmt.Sprintf("invalid skill agent %q; expected codex, claude, or both", value),
						map[string]any{"agent": value, "allowed": []string{"codex", "claude", "both"}},
					)
				}
			}
		case "--yes", "--dry-run", "--force":
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			remaining = remaining[1:]
			switch name {
			case "--yes":
				input.Yes = true
			case "--dry-run":
				input.DryRun = true
			case "--force":
				input.Force = true
			}
		default:
			return nil, unknownFlag(name)
		}
	}
	return input, nil
}

type skillInstallData struct {
	Status         string                  `json:"status"`
	Scope          app.SkillScope          `json:"scope,omitempty"`
	Agent          app.SkillAgent          `json:"agent,omitempty"`
	RepositoryRoot string                  `json:"repository_root,omitempty"`
	Targets        []app.SkillTargetResult `json:"targets"`
	Content        string                  `json:"content,omitempty"`
}

func (data skillInstallData) RenderHuman(writer io.Writer) error {
	switch data.Status {
	case "cancelled":
		_, err := io.WriteString(writer, "Installation cancelled; no files were written.\n")
		return err
	case "dry_run":
		if _, err := io.WriteString(writer, "Dry run; no files were written.\n"); err != nil {
			return err
		}
		for _, target := range data.Targets {
			if _, err := fmt.Fprintf(writer, "  %s  %s  %s\n", titleSkillAgent(target.Agent), target.Result, target.Path); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(writer, "\n%s", data.Content)
		return err
	default:
		for _, target := range data.Targets {
			if _, err := fmt.Fprintf(writer, "%s  %s  %s\n", titleSkillAgent(target.Agent), target.Result, target.Path); err != nil {
				return err
			}
		}
		return nil
	}
}

func runSkillInstall(ctx context.Context, installer app.SkillInstaller, invocation Invocation) (skillInstallData, error) {
	input := invocation.Input.(skillInput)
	interactive := invocation.Interactive && invocation.Globals.Human
	if (input.Scope == "" || input.Agent == "") && !interactive {
		missing := make([]string, 0, 2)
		if input.Scope == "" {
			missing = append(missing, "--scope")
		}
		if input.Agent == "" {
			missing = append(missing, "--agent")
		}
		return skillInstallData{}, domain.NewError(
			domain.Usage,
			"missing_skill_choices",
			"skill install requires --scope and --agent when prompts are unavailable",
			map[string]any{"missing": missing},
		)
	}

	environment, err := installer.Environment(ctx, invocation.WorkingDirectory)
	if err != nil {
		return skillInstallData{}, err
	}
	wizard := newSkillWizard(invocation.Stdin, invocation.Stdout)
	if input.Scope == "" {
		if environment.RepositoryRoot == "" {
			if err := wizard.repositoryUnavailable(environment.HomeRoot); err != nil {
				return skillInstallData{}, err
			}
			input.Scope = app.SkillScopePersonal
		} else {
			input.Scope, err = wizard.chooseScope(environment)
			if err != nil {
				return skillInstallData{}, err
			}
			if input.Scope == "" {
				return cancelledSkillData(input, nil), nil
			}
		}
	}
	if input.Scope == app.SkillScopeRepository && environment.RepositoryRoot == "" {
		return skillInstallData{}, domain.NewError(
			domain.Usage,
			"repository_scope_unavailable",
			"repository scope requires the current directory to be inside a Git work tree",
			map[string]any{"working_directory": invocation.WorkingDirectory},
		)
	}
	if input.Agent == "" {
		input.Agent, err = wizard.chooseAgent()
		if err != nil {
			return skillInstallData{}, err
		}
		if input.Agent == "" {
			return cancelledSkillData(input, nil), nil
		}
	}

	plan, err := installer.Plan(environment, input.Scope, input.Agent)
	if err != nil {
		return skillInstallData{}, err
	}
	if input.DryRun {
		return dryRunSkillData(plan, input.Force), nil
	}

	if interactive {
		if err := wizard.showPlan(plan); err != nil {
			return skillInstallData{}, err
		}
	}
	allowReplacement := input.Force
	if len(skillPlanConflicts(plan)) > 0 && !allowReplacement {
		if !interactive {
			return skillInstallData{}, installer.ConflictError(plan)
		}
		allowReplacement, err = wizard.confirm("Replace every differing existing skill file? [y/N]: ")
		if err != nil {
			return skillInstallData{}, err
		}
		if !allowReplacement {
			return cancelledSkillData(input, &plan), nil
		}
	}
	if !input.Yes {
		if !interactive {
			return skillInstallData{}, domain.NewError(
				domain.Confirmation,
				"confirmation_required",
				"skill installation requires --yes when an interactive confirmation is unavailable",
				map[string]any{"scope": input.Scope, "agent": input.Agent},
			)
		}
		confirmed, err := wizard.confirm("Install the Pellets skill at every displayed destination? [y/N]: ")
		if err != nil {
			return skillInstallData{}, err
		}
		if !confirmed {
			return cancelledSkillData(input, &plan), nil
		}
	}

	result, err := installer.Apply(plan, allowReplacement)
	if err != nil {
		return skillInstallData{}, err
	}
	return skillInstallData{
		Status: result.Status, Scope: input.Scope, Agent: input.Agent,
		RepositoryRoot: plan.RepositoryRoot, Targets: result.Targets,
	}, nil
}

type skillWizard struct {
	reader *bufio.Reader
	writer io.Writer
}

func newSkillWizard(reader io.Reader, writer io.Writer) *skillWizard {
	return &skillWizard{reader: bufio.NewReader(reader), writer: writer}
}

func (wizard *skillWizard) repositoryUnavailable(home string) error {
	_, err := fmt.Fprintf(
		wizard.writer,
		"Repository scope is unavailable because the current directory is not inside a Git work tree.\nUsing Personal scope rooted at %s.\n\n",
		home,
	)
	return err
}

func (wizard *skillWizard) chooseScope(environment app.SkillEnvironment) (app.SkillScope, error) {
	if _, err := fmt.Fprintf(
		wizard.writer,
		"Git repository root: %s\nChoose installation scope:\n  1) Repository\n  2) Personal (%s)\n  0) Cancel\nScope: ",
		environment.RepositoryRoot,
		environment.HomeRoot,
	); err != nil {
		return "", err
	}
	for {
		answer, err := wizard.readAnswer()
		if err != nil {
			return "", err
		}
		switch strings.ToLower(answer) {
		case "1", "repo", "repository":
			return app.SkillScopeRepository, nil
		case "2", "personal":
			return app.SkillScopePersonal, nil
		case "", "0", "cancel", "c", "q", "quit":
			return "", nil
		default:
			if _, err := io.WriteString(wizard.writer, "Enter 1 for Repository, 2 for Personal, or 0 to cancel: "); err != nil {
				return "", err
			}
		}
	}
}

func (wizard *skillWizard) chooseAgent() (app.SkillAgent, error) {
	if _, err := io.WriteString(
		wizard.writer,
		"Choose agent target:\n  1) Codex\n  2) Claude\n  3) Both\n  0) Cancel\nAgent: ",
	); err != nil {
		return "", err
	}
	for {
		answer, err := wizard.readAnswer()
		if err != nil {
			return "", err
		}
		switch strings.ToLower(answer) {
		case "1", "codex":
			return app.SkillAgentCodex, nil
		case "2", "claude":
			return app.SkillAgentClaude, nil
		case "3", "both":
			return app.SkillAgentBoth, nil
		case "", "0", "cancel", "c", "q", "quit":
			return "", nil
		default:
			if _, err := io.WriteString(wizard.writer, "Enter 1 for Codex, 2 for Claude, 3 for Both, or 0 to cancel: "); err != nil {
				return "", err
			}
		}
	}
}

func (wizard *skillWizard) showPlan(plan app.SkillPlan) error {
	if _, err := fmt.Fprintf(wizard.writer, "\nScope: %s\n", titleSkillScope(plan.Scope)); err != nil {
		return err
	}
	if plan.RepositoryRoot != "" {
		if _, err := fmt.Fprintf(wizard.writer, "Repository root: %s\n", plan.RepositoryRoot); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(wizard.writer, "Destinations:\n"); err != nil {
		return err
	}
	for _, target := range plan.Targets {
		if _, err := fmt.Fprintf(wizard.writer, "  %s: %s", titleSkillAgent(target.Agent), target.Path); err != nil {
			return err
		}
		if target.State == "different" {
			if _, err := io.WriteString(wizard.writer, " (different existing file)"); err != nil {
				return err
			}
		} else if target.State == "identical" {
			if _, err := io.WriteString(wizard.writer, " (already current)"); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(wizard.writer, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func (wizard *skillWizard) confirm(prompt string) (bool, error) {
	if _, err := io.WriteString(wizard.writer, prompt); err != nil {
		return false, err
	}
	for {
		answer, err := wizard.readAnswer()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "y", "yes":
			return true, nil
		case "", "n", "no", "0", "cancel", "c", "q", "quit":
			return false, nil
		default:
			if _, err := io.WriteString(wizard.writer, "Enter yes to continue or no to cancel: "); err != nil {
				return false, err
			}
		}
	}
}

func (wizard *skillWizard) readAnswer() (string, error) {
	line, err := wizard.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func dryRunSkillData(plan app.SkillPlan, force bool) skillInstallData {
	targets := make([]app.SkillTargetResult, len(plan.Targets))
	for index, target := range plan.Targets {
		result := "would_install"
		switch target.State {
		case "identical":
			result = "idempotent"
		case "different":
			if force {
				result = "would_replace"
			} else {
				result = "would_conflict"
			}
		}
		targets[index] = app.SkillTargetResult{Agent: target.Agent, Path: target.Path, Result: result}
	}
	return skillInstallData{
		Status: "dry_run", Scope: plan.Scope, Agent: plan.Agent,
		RepositoryRoot: plan.RepositoryRoot, Targets: targets, Content: plan.Content,
	}
}

func cancelledSkillData(input skillInput, plan *app.SkillPlan) skillInstallData {
	var targets []app.SkillTargetPlan
	repositoryRoot := ""
	if plan != nil {
		targets = plan.Targets
		repositoryRoot = plan.RepositoryRoot
	}
	results := make([]app.SkillTargetResult, len(targets))
	for index, target := range targets {
		results[index] = app.SkillTargetResult{Agent: target.Agent, Path: target.Path, Result: "cancelled"}
	}
	return skillInstallData{
		Status: "cancelled", Scope: input.Scope, Agent: input.Agent,
		RepositoryRoot: repositoryRoot, Targets: results,
	}
}

func skillPlanConflicts(plan app.SkillPlan) []app.SkillTargetPlan {
	conflicts := make([]app.SkillTargetPlan, 0)
	for _, target := range plan.Targets {
		if target.State == "different" {
			conflicts = append(conflicts, target)
		}
	}
	return conflicts
}

func titleSkillAgent(agent app.SkillAgent) string {
	switch agent {
	case app.SkillAgentCodex:
		return "Codex"
	case app.SkillAgentClaude:
		return "Claude"
	default:
		return string(agent)
	}
}

func titleSkillScope(scope app.SkillScope) string {
	if scope == app.SkillScopeRepository {
		return "Repository"
	}
	return "Personal"
}
