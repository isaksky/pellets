package codex

import (
	"context"
	"os"
)

type custodyKey struct{}

// WithExecutionLock binds every child invocation, including preflight probes,
// to a foreground execution lock. The caller owns the descriptor until Close.
// On Unix an independent custodian inherits it and cleans up on server death;
// Windows already contains every invocation in a kill-on-close Job Object.
func WithExecutionLock(ctx context.Context, file *os.File) context.Context {
	return context.WithValue(ctx, custodyKey{}, file)
}
