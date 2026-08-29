package main

import (
	"context"
	"errors"
	"io"
	"os"

	"pellets/internal/app"
	"pellets/internal/cli"
	"pellets/internal/discovery"
	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
	"pellets/internal/webui"
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
	webRunner := webui.Runner{
		OpenApplication: func(ctx context.Context, databaseRoot, databasePath, workingDirectory string) (*app.WebApplication, error) {
			database := app.Database{Root: databaseRoot, Path: databasePath}
			var current *storage.ResolvedProject
			resolved, resolveErr := projectManager.ResolveCurrent(ctx, database, workingDirectory)
			if resolveErr == nil {
				current = &resolved
			} else {
				code := domain.PublicError(resolveErr).Code
				if code != "git_repository_not_found" && code != "project_not_registered" && code != "workspace_not_registered" {
					return nil, resolveErr
				}
			}
			writer, err := sqlite.OpenWebWriter(ctx, databasePath)
			if err != nil {
				return nil, err
			}
			reader, err := sqlite.OpenWebReader(ctx, databasePath)
			if err != nil {
				_ = writer.Close()
				return nil, err
			}
			return &app.WebApplication{Reader: reader, Writer: writer, Current: current}, nil
		},
		OpenMonitor: func(ctx context.Context, databasePath string) (webui.Monitor, error) {
			return sqlite.OpenDataVersionMonitor(ctx, databasePath)
		},
		OpenBrowser: webui.OpenDefaultBrowser,
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
		cli.WebCommand(func(ctx context.Context, invocation cli.Invocation, options cli.WebOptions, stdout, stderr io.Writer) error {
			if invocation.Database == nil {
				return errors.New("web command database discovery did not run")
			}
			return webRunner.Run(ctx, webui.Options{
				DatabaseRoot: invocation.Database.Root, DatabasePath: invocation.Database.Path, WorkingDirectory: invocation.WorkingDirectory,
				InitialProject: invocation.Globals.Project, Port: options.Port, NoOpen: options.NoOpen,
				Stdout: stdout, Stderr: stderr,
			})
		}),
	}
	application := cli.New(version, commands...)
	os.Exit(application.Run(os.Args[1:], os.Stdout, os.Stderr))
}
