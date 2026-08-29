package main

import (
	"context"
	"os"

	"pellets/internal/app"
	"pellets/internal/cli"
	"pellets/internal/discovery"
	"pellets/internal/storage/sqlite"
)

var version = "dev"

func main() {
	initializer := app.DatabaseInitializer{
		Open: func(ctx context.Context, path string) (app.DatabaseHandle, error) {
			return sqlite.Open(ctx, path)
		},
		GitSafety: discovery.GitSafety{},
	}
	application := cli.New(version, cli.InitDBCommand(initializer))
	os.Exit(application.Run(os.Args[1:], os.Stdout, os.Stderr))
}
