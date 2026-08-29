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
	pelletManager := app.PelletManager{
		Projects: projectManager,
		Open: func(ctx context.Context, path string) (storage.PelletRepository, error) {
			return sqlite.OpenPelletRepository(ctx, path)
		},
	}
	memoryManager := app.MemoryManager{
		Projects: projectManager,
		Open: func(ctx context.Context, path string) (storage.MemoryRepository, error) {
			return sqlite.OpenMemoryRepository(ctx, path)
		},
	}
	commands := []cli.Command{
		cli.InitDBCommand(initializer),
		cli.InitCommand(projectManager),
		cli.ProjectCommand(projectManager),
		cli.AddCommand(pelletManager),
		cli.MoveCommand(pelletManager),
		cli.ListCommand(pelletManager),
		cli.SearchCommand(pelletManager),
		cli.PurgeCommand(pelletManager),
		cli.ShowCommand(pelletManager),
		cli.EditCommand(pelletManager),
		cli.NextCommand(pelletManager),
		cli.StartCommand(pelletManager),
		cli.StartNextCommand(pelletManager),
		cli.ReleaseCommand(pelletManager),
		cli.CloseCommand(pelletManager),
		cli.ReopenCommand(pelletManager),
		cli.DeferCommand(pelletManager),
		cli.MemoryCommand(memoryManager),
	}
	application := cli.New(version, commands...)
	os.Exit(application.Run(os.Args[1:], os.Stdout, os.Stderr))
}
