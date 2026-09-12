package app

import (
	"context"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func (application *WebApplication) UpdateCheckpointScope(ctx context.Context, project storage.Project, reference domain.PelletReference, version string, targets []storage.ReviewTargetVersion) (storage.Pellet, error) {
	writer, ok := application.Writer.(storage.CheckpointWriter)
	if !ok {
		return storage.Pellet{}, checkpointManagementUnavailable()
	}
	return writer.UpdateWebCheckpointScope(ctx, project, reference, version, targets)
}

func (application *WebApplication) RemoveCheckpoint(ctx context.Context, project storage.Project, reference domain.PelletReference, version string) (storage.Pellet, error) {
	writer, ok := application.Writer.(storage.CheckpointWriter)
	if !ok {
		return storage.Pellet{}, checkpointManagementUnavailable()
	}
	return writer.RemoveWebCheckpoint(ctx, project, reference, version)
}

func (application *WebApplication) RestoreCheckpoint(ctx context.Context, project storage.Project, reference domain.PelletReference, version string) (storage.Pellet, error) {
	writer, ok := application.Writer.(storage.CheckpointWriter)
	if !ok {
		return storage.Pellet{}, checkpointManagementUnavailable()
	}
	return writer.RestoreWebCheckpoint(ctx, project, reference, version)
}

func (application *WebApplication) CheckpointHistory(ctx context.Context, project storage.Project, reference domain.PelletReference) ([]storage.CheckpointScopeHistory, error) {
	reader, ok := application.Reader.(storage.CheckpointHistoryReader)
	if !ok {
		return nil, nil
	}
	return reader.ReadWebCheckpointHistory(ctx, project, reference)
}

func checkpointManagementUnavailable() error {
	return domain.NewError(domain.Conflict, "checkpoint_management_unavailable", "checkpoint management is unavailable", nil)
}
