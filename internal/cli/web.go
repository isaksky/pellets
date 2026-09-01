package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"pellets/internal/domain"
)

type WebOptions struct {
	Port   uint16
	NoOpen bool
}

type WebRunner func(context.Context, Invocation, WebOptions, io.Writer, io.Writer) error

// WebCommand creates the only foreground/raw-output command. Its stdout is
// the listener URL; it does not emit a JSON envelope after shutdown.
func WebCommand(run WebRunner) Command {
	return Command{
		Name:                  "web",
		Summary:               "Open the foreground local web inspector.",
		Usage:                 "pl [--project CODE] web [--port PORT] [--no-open]",
		Parse:                 parseWebOptions,
		NeedsCurrentWorkspace: alwaysNeedsCurrentWorkspace,
		Validate: func(globals GlobalOptions, _ any) error {
			if globals.Human || globals.Pretty {
				return domain.NewError(domain.Usage, "format_not_supported", "web does not use JSON or human output formatting", nil)
			}
			if run == nil {
				return domain.NewError(domain.Unexpected, "internal_error", "web command is not configured", nil)
			}
			return nil
		},
		RunForeground: func(ctx context.Context, invocation Invocation, stdout, stderr io.Writer) error {
			notifyContext, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()
			return run(notifyContext, invocation, invocation.Input.(WebOptions), stdout, stderr)
		},
	}
}

func parseWebOptions(arguments []string) (any, error) {
	var options WebOptions
	seen := make(map[string]bool)
	for len(arguments) > 0 {
		name, value, hasValue := splitOption(arguments[0])
		if !strings.HasPrefix(name, "-") {
			return nil, unexpectedArgument(name)
		}
		if seen[name] {
			return nil, domain.NewError(domain.Usage, "duplicate_flag", fmt.Sprintf("flag %q may only be specified once", name), map[string]any{"flag": name})
		}
		seen[name] = true
		switch name {
		case "--no-open":
			if hasValue {
				return nil, flagTakesNoValue(name)
			}
			options.NoOpen = true
		case "--port":
			if !hasValue {
				if len(arguments) < 2 || strings.HasPrefix(arguments[1], "-") {
					return nil, missingFlagValue(name)
				}
				value = arguments[1]
				arguments = arguments[1:]
			}
			port, err := strconv.ParseUint(value, 10, 16)
			if err != nil || strconv.FormatUint(port, 10) != value {
				return nil, domain.NewError(domain.Usage, "invalid_port", "port must be a canonical integer from 0 through 65535", map[string]any{"port": value})
			}
			options.Port = uint16(port)
		default:
			return nil, unknownFlag(name)
		}
		arguments = arguments[1:]
	}
	return options, nil
}
