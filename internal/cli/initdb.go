package cli

import (
	"context"
	"fmt"
	"io"

	"pellets/internal/app"
)

// InitDBCommand creates the database-only initialization command.
func InitDBCommand(initializer app.DatabaseInitializer) Command {
	return Command{
		Name:                  "init-db",
		Summary:               "Create a database in the current directory.",
		Usage:                 "pl init-db",
		SkipDatabaseDiscovery: true,
		Validate: func(globals GlobalOptions, _ any) error {
			if globals.Project != "" {
				return projectNotAllowed("init-db")
			}
			return nil
		},
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			initialized, err := initializer.Init(ctx, invocation.WorkingDirectory)
			if err != nil {
				return nil, err
			}
			return initDBData{DatabasePath: initialized.Path}, nil
		},
	}
}

type initDBData struct {
	DatabasePath string `json:"database_path"`
}

func (data initDBData) RenderHuman(w io.Writer) error {
	_, err := fmt.Fprintf(w, "Created %s\n", data.DatabasePath)
	return err
}
