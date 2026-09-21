package codex

import (
	"context"
	"errors"
	"time"
)

// GlobalModels uses the server default runtime/account, without workspace
// settings, access policies or a model turn. Preflight remains run-specific.
func GlobalModels(ctx context.Context) (models []ModelInfo, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	settings, err := resolveLocalRunSettings(WorkspaceRunSettings{}, RunOverrides{})
	if err != nil {
		return nil, err
	}
	client, err := Start(ctx, Config{Executable: settings.Executable, MaxMessageBytes: 1 << 20, EventBuffer: 128, MaxPending: 16, StderrBytes: 32 << 10})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	account, err := readAccount(ctx, client)
	if err != nil {
		return nil, err
	}
	if !account.Ready {
		return nil, ErrUnauthenticated
	}
	return readModels(ctx, client)
}
