package app

import (
	"context"
	"errors"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type GroupRepositoryOpener func(context.Context, string) (storage.GroupRepository, error)

// GroupManager uses the same logical project/worktree resolution as pellets and
// memory. Callers edit by stable group ID and the last observed group revision.
type GroupManager struct {
	Projects ProjectManager
	Open     GroupRepositoryOpener
}

func withGroups[T any](ctx context.Context, m GroupManager, db Database, cwd, code string, f func(storage.GroupRepository, storage.Project) (T, error)) (T, error) {
	var zero T
	if m.Open == nil {
		return zero, domain.NewError(domain.Unexpected, "internal_error", "group manager is not configured", nil)
	}
	selected, err := m.Projects.ResolveSelectedCurrentProject(ctx, db, cwd, code)
	if err != nil {
		return zero, err
	}
	repository, err := m.Open(ctx, db.Path)
	if err != nil {
		return zero, err
	}
	value, operationErr := f(repository, selected.Project)
	return value, errors.Join(operationErr, repository.Close())
}
func (m GroupManager) Create(ctx context.Context, db Database, cwd, code, name string) (storage.Group, error) {
	return withGroups(ctx, m, db, cwd, code, func(r storage.GroupRepository, p storage.Project) (storage.Group, error) {
		return r.CreateGroup(ctx, p, name)
	})
}
func (m GroupManager) List(ctx context.Context, db Database, cwd, code string) ([]storage.Group, error) {
	return withGroups(ctx, m, db, cwd, code, func(r storage.GroupRepository, p storage.Project) ([]storage.Group, error) {
		return r.ListGroups(ctx, p)
	})
}
func (m GroupManager) Read(ctx context.Context, db Database, cwd, code string, id int64) (storage.Group, error) {
	return withGroups(ctx, m, db, cwd, code, func(r storage.GroupRepository, p storage.Project) (storage.Group, error) {
		return r.ReadGroup(ctx, p, id)
	})
}
func (m GroupManager) EditContext(ctx context.Context, db Database, cwd, code string, id, revision int64, markdown string) (storage.Group, error) {
	return withGroups(ctx, m, db, cwd, code, func(r storage.GroupRepository, p storage.Project) (storage.Group, error) {
		return r.EditGroupContext(ctx, p, id, revision, markdown)
	})
}
func (m GroupManager) Rename(ctx context.Context, db Database, cwd, code string, id, revision int64, name string) (storage.Group, error) {
	return withGroups(ctx, m, db, cwd, code, func(r storage.GroupRepository, p storage.Project) (storage.Group, error) {
		return r.RenameGroup(ctx, p, id, revision, name)
	})
}

func (a *WebApplication) ListGroups(ctx context.Context, p storage.Project) ([]storage.Group, error) {
	r, ok := a.Reader.(storage.GroupReader)
	if !ok {
		return nil, groupsUnavailable()
	}
	return r.ListGroups(ctx, p)
}
func (a *WebApplication) ReadGroup(ctx context.Context, p storage.Project, id int64) (storage.Group, error) {
	r, ok := a.Reader.(storage.GroupReader)
	if !ok {
		return storage.Group{}, groupsUnavailable()
	}
	return r.ReadGroup(ctx, p, id)
}
func (a *WebApplication) CreateGroup(ctx context.Context, p storage.Project, name string) (storage.Group, error) {
	w, ok := a.Writer.(storage.GroupWriter)
	if !ok {
		return storage.Group{}, groupsUnavailable()
	}
	return w.CreateGroup(ctx, p, name)
}
func (a *WebApplication) EditGroupContext(ctx context.Context, p storage.Project, id, revision int64, markdown string) (storage.Group, error) {
	w, ok := a.Writer.(storage.GroupWriter)
	if !ok {
		return storage.Group{}, groupsUnavailable()
	}
	return w.EditGroupContext(ctx, p, id, revision, markdown)
}
func (a *WebApplication) RenameGroup(ctx context.Context, p storage.Project, id, revision int64, name string) (storage.Group, error) {
	w, ok := a.Writer.(storage.GroupWriter)
	if !ok {
		return storage.Group{}, groupsUnavailable()
	}
	return w.RenameGroup(ctx, p, id, revision, name)
}
func groupsUnavailable() error {
	return domain.NewError(domain.Unexpected, "groups_unavailable", "project groups are unavailable", nil)
}
