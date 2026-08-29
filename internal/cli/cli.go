// Package cli parses invocations and maps command results onto the output contract.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/output"
)

const productDescription = "Pellets is a local task queue for coding agents."

// GlobalOptions are parsed before a command name and apply to every command.
type GlobalOptions struct {
	Human   bool
	Pretty  bool
	Project string
}

// Invocation is the strictly parsed input passed to a command handler.
type Invocation struct {
	Globals          GlobalOptions
	Input            any
	WorkingDirectory string
	Database         *discovery.Database
}

// Command defines one CLI command. Parse must reject every unsupported flag and
// positional argument. Validate must reject usage errors that depend on parsed
// globals and command input. Neither function may perform discovery or command
// side effects. Run does not render or write process output.
type Command struct {
	Name                  string
	Summary               string
	Usage                 string
	SkipDatabaseDiscovery bool
	Parse                 func(args []string) (any, error)
	Validate              func(globals GlobalOptions, input any) error
	Run                   func(ctx context.Context, invocation Invocation) (any, error)
	ResultName            func(input any) string
}

// App parses and dispatches commands and performs their database selection.
type App struct {
	version          string
	commands         map[string]Command
	workingDirectory func() (string, error)
}

// New creates an application with the supplied commands.
func New(version string, commands ...Command) *App {
	return NewWithCommands(version, commands...)
}

// NewWithCommands creates an application with the supplied command handlers.
// Later milestones register their commands here; tests use it as the command harness.
func NewWithCommands(version string, commands ...Command) *App {
	registered := make(map[string]Command, len(commands))
	for _, command := range commands {
		if command.Name == "" {
			panic("cli: command has an empty name")
		}
		if _, exists := registered[command.Name]; exists {
			panic("cli: duplicate command " + command.Name)
		}
		registered[command.Name] = command
	}
	return &App{version: version, commands: registered, workingDirectory: os.Getwd}
}

// Run executes one CLI invocation and returns its process exit code.
func (a *App) Run(args []string, stdout, stderr io.Writer) int {
	parsed, err := a.parse(args)
	if err != nil {
		return writeFailure(stderr, err)
	}

	switch parsed.action {
	case actionHelp:
		_, err = io.WriteString(stdout, a.help())
	case actionVersion:
		_, err = fmt.Fprintf(stdout, "pl %s (JSON schema %d)\n", a.version, output.SchemaVersion)
	case actionCommandHelp:
		_, err = io.WriteString(stdout, commandHelp(parsed.command))
	case actionRun:
		var input any
		if parsed.command.Parse == nil {
			input, err = ParseNoArguments(parsed.args)
		} else {
			input, err = parsed.command.Parse(parsed.args)
		}
		if err == nil {
			err = validateGlobalOptions(parsed.globals)
		}
		if err == nil && parsed.command.Validate != nil {
			err = parsed.command.Validate(parsed.globals, input)
		}
		if err == nil {
			if parsed.command.Run == nil {
				err = domain.NewError(domain.Unexpected, "internal_error", "command has no handler", nil)
			} else {
				invocation := Invocation{Globals: parsed.globals, Input: input}
				invocation.WorkingDirectory, err = a.workingDirectory()
				if err != nil {
					err = domain.WrapError(
						domain.Unexpected,
						"working_directory_unavailable",
						"could not determine the current working directory",
						nil,
						err,
					)
				}
				if err == nil && !parsed.command.SkipDatabaseDiscovery {
					var database discovery.Database
					database, err = discovery.FindDatabase(invocation.WorkingDirectory)
					if err == nil {
						invocation.Database = &database
					}
				}

				var data any
				if err == nil {
					data, err = parsed.command.Run(context.Background(), invocation)
				}
				if err == nil {
					renderer := output.Renderer(output.JSONRenderer{Pretty: parsed.globals.Pretty})
					if parsed.globals.Human {
						renderer = output.HumanRenderer{}
					}
					resultName := parsed.command.Name
					if parsed.command.ResultName != nil {
						resultName = parsed.command.ResultName(input)
					}
					err = renderer.Render(stdout, resultName, data)
				}
			}
		}
	}

	if err != nil {
		if output.IsWriteFailure(err) || parsed.action == actionHelp || parsed.action == actionVersion || parsed.action == actionCommandHelp {
			return 1
		}
		return writeFailure(stderr, err)
	}
	return 0
}

// ParseNoArguments is a strict parser for commands that accept no options or arguments.
func ParseNoArguments(args []string) (any, error) {
	if len(args) == 0 {
		return struct{}{}, nil
	}
	if strings.HasPrefix(args[0], "-") {
		return nil, unknownFlag(args[0])
	}
	return nil, unexpectedArgument(args[0])
}

type action uint8

const (
	actionRun action = iota
	actionHelp
	actionVersion
	actionCommandHelp
)

type parsedInvocation struct {
	action  action
	globals GlobalOptions
	command Command
	args    []string
}

func (a *App) parse(args []string) (parsedInvocation, error) {
	var parsed parsedInvocation
	seen := make(map[string]bool)

	for len(args) > 0 {
		arg := args[0]
		if !strings.HasPrefix(arg, "-") {
			if err := validateFormats(parsed); err != nil {
				return parsedInvocation{}, err
			}
			if parsed.action == actionHelp || parsed.action == actionVersion {
				return parsedInvocation{}, unexpectedArgument(arg)
			}
			command, ok := a.commands[arg]
			if !ok {
				return parsedInvocation{}, domain.NewError(
					domain.Usage,
					"unknown_command",
					fmt.Sprintf("unknown command %q", arg),
					map[string]any{"command": arg},
				)
			}
			parsed.action = actionRun
			parsed.command = command
			parsed.args = args[1:]
			if len(parsed.args) == 1 && parsed.args[0] == "--help" {
				parsed.action = actionCommandHelp
			}
			return parsed, validateFormats(parsed)
		}

		name, value, hasValue := splitOption(arg)
		if seen[name] {
			return parsedInvocation{}, domain.NewError(
				domain.Usage,
				"duplicate_flag",
				fmt.Sprintf("flag %q may only be specified once", name),
				map[string]any{"flag": name},
			)
		}
		seen[name] = true

		switch name {
		case "--human":
			if hasValue {
				return parsedInvocation{}, flagTakesNoValue(name)
			}
			parsed.globals.Human = true
		case "--pretty":
			if hasValue {
				return parsedInvocation{}, flagTakesNoValue(name)
			}
			parsed.globals.Pretty = true
		case "--project":
			if !hasValue {
				if len(args) < 2 || strings.HasPrefix(args[1], "-") {
					return parsedInvocation{}, missingFlagValue(name)
				}
				value = args[1]
				args = args[1:]
			}
			if value == "" {
				return parsedInvocation{}, missingFlagValue(name)
			}
			parsed.globals.Project = value
		case "--help":
			if hasValue {
				return parsedInvocation{}, flagTakesNoValue(name)
			}
			if parsed.action == actionVersion {
				return parsedInvocation{}, conflictingFlags("--help", "--version")
			}
			parsed.action = actionHelp
		case "--version":
			if hasValue {
				return parsedInvocation{}, flagTakesNoValue(name)
			}
			if parsed.action == actionHelp {
				return parsedInvocation{}, conflictingFlags("--help", "--version")
			}
			parsed.action = actionVersion
		default:
			return parsedInvocation{}, unknownFlag(name)
		}
		args = args[1:]
	}

	if err := validateFormats(parsed); err != nil {
		return parsedInvocation{}, err
	}
	if parsed.action == actionHelp || parsed.action == actionVersion {
		return parsed, nil
	}
	return parsedInvocation{}, domain.NewError(
		domain.Usage,
		"missing_command",
		"a command is required",
		nil,
	)
}

func validateFormats(parsed parsedInvocation) error {
	if parsed.globals.Human && parsed.globals.Pretty {
		return conflictingFlags("--human", "--pretty")
	}
	return nil
}

func validateGlobalOptions(globals GlobalOptions) error {
	if globals.Project == "" {
		return nil
	}
	return domain.ValidateProjectCode(globals.Project)
}

func conflictingFlags(first, second string) error {
	return domain.NewError(
		domain.Usage,
		"conflicting_flags",
		fmt.Sprintf("%s and %s are mutually exclusive", first, second),
		map[string]any{"flags": []string{first, second}},
	)
}

func splitOption(arg string) (name, value string, hasValue bool) {
	name, value, hasValue = strings.Cut(arg, "=")
	return name, value, hasValue
}

func unknownFlag(flag string) error {
	return domain.NewError(
		domain.Usage,
		"unknown_flag",
		fmt.Sprintf("unknown flag %q", flag),
		map[string]any{"flag": flag},
	)
}

func unexpectedArgument(argument string) error {
	return domain.NewError(
		domain.Usage,
		"unexpected_argument",
		fmt.Sprintf("unexpected argument %q", argument),
		map[string]any{"argument": argument},
	)
}

func missingFlagValue(flag string) error {
	return domain.NewError(
		domain.Usage,
		"missing_flag_value",
		fmt.Sprintf("flag %q requires a value", flag),
		map[string]any{"flag": flag},
	)
}

func flagTakesNoValue(flag string) error {
	return domain.NewError(
		domain.Usage,
		"unexpected_flag_value",
		fmt.Sprintf("flag %q does not accept a value", flag),
		map[string]any{"flag": flag},
	)
}

func writeFailure(stderr io.Writer, err error) int {
	if writeErr := output.WriteError(stderr, err); writeErr != nil {
		return 1
	}
	return domain.ExitCode(err)
}

func (a *App) help() string {
	var builder strings.Builder
	builder.WriteString(productDescription)
	builder.WriteString("\n\nUsage:\n  pl [global-options] <command> [command-options] [arguments]\n")

	if len(a.commands) > 0 {
		builder.WriteString("\nCommands:\n")
		names := make([]string, 0, len(a.commands))
		for name := range a.commands {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			command := a.commands[name]
			fmt.Fprintf(&builder, "  %-12s %s\n", command.Name, command.Summary)
		}
	}

	builder.WriteString(`
Global options:
  --human        Render concise human-readable text instead of JSON.
  --pretty       Pretty-print JSON (mutually exclusive with --human).
  --project CODE Select a registered project where the command permits it.
  --help         Print help and exit.
  --version      Print executable and JSON schema versions.
`)
	return builder.String()
}

func commandHelp(command Command) string {
	if command.Usage != "" {
		return fmt.Sprintf("Usage:\n  %s\n", command.Usage)
	}
	return fmt.Sprintf("Usage:\n  pl %s\n", command.Name)
}
