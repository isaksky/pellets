package app

import (
	"context"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

// WebApplication is the long-lived application boundary for the local web
// tool. Reader and writer lifetimes are server-scoped, but every returned row
// is fully materialized before this layer hands it to HTTP rendering.
type WebApplication struct {
	Reader  storage.WebReader
	Writer  storage.WebWriter
	Current *storage.ResolvedProject
}

func (application *WebApplication) Close() error {
	return errors.Join(application.Reader.Close(), application.Writer.Close())
}

func (application *WebApplication) Projects(ctx context.Context) ([]storage.WebProjectSummary, error) {
	return application.Reader.ListWebProjects(ctx)
}

func (application *WebApplication) Project(ctx context.Context, code string) (storage.WebProjectSummary, error) {
	if err := domain.ValidateProjectCode(code); err != nil {
		return storage.WebProjectSummary{}, err
	}
	projects, err := application.Projects(ctx)
	if err != nil {
		return storage.WebProjectSummary{}, err
	}
	for _, project := range projects {
		if project.Project.Code == code || projectHasRedirect(project.Project, code) {
			return project, nil
		}
	}
	return storage.WebProjectSummary{}, domain.NewError(domain.NotFound, "project_not_registered", "the project is not registered in the selected Pellets database", map[string]any{"code": code})
}

func (application *WebApplication) Pellets(ctx context.Context, project storage.Project, filters storage.WebPelletFilters) ([]storage.Pellet, error) {
	return application.Reader.ListWebPellets(ctx, project, filters)
}

func (application *WebApplication) Pellet(ctx context.Context, project storage.Project, reference domain.PelletReference) (storage.Pellet, error) {
	return application.Reader.ReadWebPellet(ctx, project, reference)
}

func (application *WebApplication) Groups(ctx context.Context, project storage.Project) ([]*string, error) {
	return application.Reader.ListWebGroups(ctx, project)
}

func (application *WebApplication) Memories(ctx context.Context, project storage.Project) ([]storage.Memory, error) {
	return application.Reader.ListWebMemories(ctx, project)
}

func (application *WebApplication) Memory(ctx context.Context, project storage.Project, memoryID int64) (storage.Memory, error) {
	return application.Reader.ReadWebMemory(ctx, project, memoryID)
}

func (application *WebApplication) CreatePellet(ctx context.Context, project storage.Project, input storage.NewPellet) (storage.Pellet, error) {
	return application.Writer.CreateWebPellet(ctx, project, input)
}

func (application *WebApplication) UpdatePellet(ctx context.Context, project storage.Project, reference domain.PelletReference, expectedVersion string, changes storage.PelletChanges) (storage.Pellet, error) {
	return application.Writer.UpdateWebPellet(ctx, project, reference, expectedVersion, changes)
}

func (application *WebApplication) MovePellet(ctx context.Context, project storage.Project, reference domain.PelletReference, expectedVersion string, placement storage.PelletPlacement) (storage.Pellet, error) {
	return application.Writer.MoveWebPellet(ctx, project, reference, expectedVersion, placement)
}

func (application *WebApplication) TransitionPellet(ctx context.Context, project storage.Project, reference domain.PelletReference, expectedVersion string, request storage.PelletLifecycleRequest) (storage.PelletLifecycleResult, error) {
	if application.Current == nil || application.Current.Project.ID != project.ID {
		return storage.PelletLifecycleResult{}, domain.NewError(
			domain.Conflict,
			"web_workspace_unavailable",
			"lifecycle controls are available only for the web server's current registered project workspace",
			map[string]any{"project": project.Code},
		)
	}
	current := *application.Current
	current.Project = project
	return application.Writer.TransitionWebPellet(ctx, current, reference, expectedVersion, request)
}

func projectHasRedirect(project storage.Project, code string) bool {
	for _, redirect := range project.Redirects {
		if redirect.Code == code {
			return true
		}
	}
	return false
}

func (application *WebApplication) CreateMemory(ctx context.Context, project storage.Project, input storage.NewMemory) (storage.Memory, error) {
	return application.Writer.CreateWebMemory(ctx, project, input)
}

func (application *WebApplication) UpdateMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion, text string) (storage.Memory, error) {
	return application.Writer.UpdateWebMemory(ctx, project, memoryID, expectedVersion, text)
}

func (application *WebApplication) ApproveMemory(ctx context.Context, project storage.Project, memoryID int64, expectedVersion string) (storage.Memory, error) {
	return application.Writer.ApproveWebMemory(ctx, project, memoryID, expectedVersion)
}
