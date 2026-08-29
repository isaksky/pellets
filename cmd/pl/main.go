package main

import (
	"context"
	"os"

	"pellets/internal/app"
	"pellets/internal/cli"
	"pellets/internal/discovery"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

var version = "dev"

func main() {
	initializer := app.DatabaseInitializer{
		Path: discovery.DatabasePath,
		Open: func(ctx context.Context, path string) (app.DatabaseHandle, error) {
			return sqlite.Open(ctx, path)
		},
		GitSafety: discovery.GitSafety{},
	}
	projectManager := app.ProjectManager{
		Discover: app.ProjectDiscovery{
			FindGitIdentity: discovery.FindGitIdentity,
			FindDatabase: func(workingDirectory string) (app.Database, error) {
				database, err := discovery.FindDatabase(workingDirectory)
				return app.Database{Root: database.Root, Path: database.Path}, err
			},
			NormalizePath: discovery.NormalizeLocalPath,
			ResolvePath:   discovery.ResolveLocalPath,
			PathExists:    discovery.PathExists,
		},
		Initialize: initializer.Init,
		Open: func(ctx context.Context, path string) (storage.ProjectDatabase, error) {
			return sqlite.OpenProjectDatabase(ctx, path)
		},
		GitSafety: discovery.GitSafety{},
	}
	application := cli.New(
		version,
		cli.InitDBCommand(initializer),
		cli.InitCommand(projectManager),
		cli.ProjectCommand(projectManager),
	)
	os.Exit(application.Run(os.Args[1:], os.Stdout, os.Stderr))
}
