package cli

import (
	"context"
	"fmt"
	"io"

	"pellets/internal/app"
	"pellets/internal/domain"
)

// InitDBCommand creates the database-only initialization command.
func InitDBCommand(initializer app.DatabaseInitializer) Command {
	return Command{
		Name:                  "init-db",
		Summary:               "Create a database in the current directory.",
		Usage:                 "pl init-db",
		SkipDatabaseDiscovery: true,
		Run: func(ctx context.Context, invocation Invocation) (any, error) {
			if invocation.Globals.Project != "" {
				return nil, domain.NewError(
					domain.Usage,
					"project_not_allowed",
					"--project is not valid for init-db",
					map[string]any{"command": "init-db"},
				)
			}
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
