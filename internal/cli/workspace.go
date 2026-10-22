package cli

import (
	"context"
	"fmt"
	"io"

	"pellets/internal/app"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

func WorkspaceCommand(manager app.ProjectManager) Command {
	return Command{Name: "workspace", Summary: "Prune registrations for missing worktrees.", Subcommands: []string{"prune"}, Usage: "pl [--project CODE] workspace prune [--dry-run]",
		Parse: func(args []string) (any, error) {
			if len(args) == 0 {
				return nil, domain.NewError(domain.Usage, "missing_subcommand", "workspace requires prune", nil)
			}
			if args[0] != "prune" {
				return nil, domain.NewError(domain.Usage, "unknown_subcommand", "workspace requires prune", nil)
			}
			dry := false
			for _, arg := range args[1:] {
				if arg != "--dry-run" {
					return nil, unknownFlag(arg)
				}
				if dry {
					return nil, duplicateCommandFlag(arg)
				}
				dry = true
			}
			return dry, nil
		},
		ResultName: func(any) string { return "workspace prune" },
		Run: func(ctx context.Context, in Invocation) (any, error) {
			db := app.Database{Root: in.Database.Root, Path: in.Database.Path}
			var project storage.Project
			var err error
			if in.Globals.Project != "" {
				project, err = manager.ShowByCode(ctx, db, in.Globals.Project)
			} else {
				project, err = manager.ShowCurrent(ctx, db, in.WorkingDirectory)
			}
			if err != nil {
				return nil, err
			}
			writer, err := manager.Open(ctx, db.Path)
			if err != nil {
				return nil, err
			}
			defer writer.Close()
			pruner, ok := writer.(storage.WorkspacePruner)
			if !ok {
				return nil, domain.NewError(domain.Unexpected, "workspace_prune_unavailable", "workspace pruning is unavailable", nil)
			}
			result, err := app.PruneWorkspaces(ctx, db, project, pruner, in.Input.(bool))
			return workspacePruneData{result}, err
		}}
}

type workspacePruneData struct{ app.WorkspacePruneResult }

func (data workspacePruneData) RenderHuman(w io.Writer) error {
	verb := "Pruned"
	if data.DryRun {
		verb = "Would prune"
	}
	if _, err := fmt.Fprintf(w, "%s %d workspace(s).\n", verb, len(data.Pruned)); err != nil {
		return err
	}
	for _, entry := range data.Pruned {
		if _, err := fmt.Fprintf(w, "  %d  %s\n", entry.ID, entry.Path); err != nil {
			return err
		}
	}
	for _, entry := range data.Skipped {
		if _, err := fmt.Fprintf(w, "  skipped %d  %s: %s\n", entry.ID, entry.Path, entry.ReasonLabel()); err != nil {
			return err
		}
	}
	return nil
}
