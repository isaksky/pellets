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

type ServerOptions struct {
	Port   uint16
	NoOpen bool
}

type ServerRunner func(context.Context, Invocation, ServerOptions, io.Writer, io.Writer) error

// ServerCommand creates the only foreground/raw-output command. Its stdout is
// the listener URL; it does not emit a JSON envelope after shutdown.
func ServerCommand(run ServerRunner) Command {
	return Command{
		Name:                  "server",
		Aliases:               []string{"web"},
		Summary:               "Run the foreground local server.",
		Usage:                 "pl [--project CODE] server [--port PORT] [--no-open]",
		Parse:                 parseServerOptions,
		NeedsCurrentWorkspace: alwaysNeedsCurrentWorkspace,
		Validate: func(globals GlobalOptions, _ any) error {
			if globals.Human || globals.Pretty {
				return domain.NewError(domain.Usage, "format_not_supported", "server does not use JSON or human output formatting", nil)
			}
			if run == nil {
				return domain.NewError(domain.Unexpected, "internal_error", "server command is not configured", nil)
			}
			return nil
		},
		RunForeground: func(ctx context.Context, invocation Invocation, stdout, stderr io.Writer) error {
			runContext, cancel := context.WithCancel(ctx)
			interrupts := make(chan os.Signal, 1)
			signal.Notify(interrupts, os.Interrupt)
			finished, signalDone := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(signalDone)
				select {
				case <-interrupts:
					// Restore the default action before starting graceful shutdown,
					// so another Ctrl+C can terminate an unresponsive process.
					signal.Stop(interrupts)
					fmt.Fprintln(stderr, "Stopping server… Press Ctrl+C again to force exit.")
					cancel()
				case <-finished:
				}
			}()
			defer func() {
				close(finished)
				signal.Stop(interrupts)
				<-signalDone
				cancel()
			}()
			return run(runContext, invocation, invocation.Input.(ServerOptions), stdout, stderr)
		},
	}
}

func parseServerOptions(arguments []string) (any, error) {
	var options ServerOptions
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

// WebOptions and WebRunner keep internal callers source-compatible while the
// public command spelling is migrated to server.
type WebOptions = ServerOptions
type WebRunner = ServerRunner

// WebCommand is retained for callers that construct the command directly.
// It returns the canonical server command and its deprecated web alias.
func WebCommand(run WebRunner) Command { return ServerCommand(run) }

func parseWebOptions(arguments []string) (any, error) { return parseServerOptions(arguments) }
