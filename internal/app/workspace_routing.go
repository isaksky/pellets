package app

import (
	"context"
	"pellets/internal/storage"
)

func (application *WebApplication) Routing(ctx context.Context, project storage.Project) (storage.ProjectRouting, error) {
	if reader, ok := application.Reader.(storage.WorkspaceRoutingReader); ok {
		return reader.ReadProjectRouting(ctx, project)
	}
	r := storage.ProjectRouting{ProjectID: project.ID, Enabled: true, Assignments: []storage.WorkspaceAssignment{}}
	for _, w := range project.Workspaces {
		r.Assignments = append(r.Assignments, storage.DefaultWorkspaceAssignment(w.ID))
	}
	return r, nil
}
func (application *WebApplication) SaveWorkspaceAssignment(ctx context.Context, project storage.Project, workspaceID int64, expectedVersion string, assignment storage.WorkspaceAssignment) (storage.ProjectRouting, error) {
	writer, ok := application.Writer.(storage.WorkspaceRoutingWriter)
	if !ok {
		return storage.ProjectRouting{}, scheduleError("workspace_assignments_unavailable", "workspace assignments are unavailable")
	}
	return writer.SaveWorkspaceAssignment(ctx, project, workspaceID, expectedVersion, assignment)
}
func (application *WebApplication) SetGroupAssignments(ctx context.Context, project storage.Project, expectedVersion string, enabled bool) (storage.ProjectRouting, error) {
	writer, ok := application.Writer.(storage.WorkspaceRoutingWriter)
	if !ok {
		return storage.ProjectRouting{}, scheduleError("workspace_assignments_unavailable", "workspace assignments are unavailable")
	}
	return writer.SetGroupAssignments(ctx, project, expectedVersion, enabled)
}
