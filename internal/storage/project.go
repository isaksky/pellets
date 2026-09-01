// Package storage defines the persistence boundaries consumed by application
// services. Concrete SQL remains in storage/sqlite.
package storage

import (
	"context"
	"time"

	"pellets/internal/domain"
)

// Workspace is one registered checkout of a logical project's Git repository.
type Workspace struct {
	ID        int64
	ProjectID int64
	RootPath  domain.LocalPath
	GitDir    domain.LocalPath
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Project is one logical Git repository and all of its registered workspaces.
type Project struct {
	ID           int64
	Code         string
	GitCommonDir domain.LocalPath
	Workspaces   []Workspace
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ResolvedProject identifies both the shared logical project and the current
// Git-worktree workspace.
type ResolvedProject struct {
	Project   Project
	Workspace Workspace
}

// ProjectRegistration contains identities resolved by Git and normalized by
// the application before the storage write transaction begins.
type ProjectRegistration struct {
	Code               string
	CodeName           string
	CodeIdentity       string
	GenerateCode       bool
	GitCommonDir       domain.LocalPath
	WorkspaceRoot      domain.LocalPath
	GitDir             domain.LocalPath
	AllowWorkspaceMove bool
}

// ProjectDatabase is the project persistence boundary for one selected
// database. Registration returns created=false for an idempotent repeat.
type ProjectDatabase interface {
	RegisterProject(ctx context.Context, registration ProjectRegistration) (project Project, created bool, err error)
	ListProjects(ctx context.Context) ([]Project, error)
	FindProjectByCode(ctx context.Context, code string) (Project, error)
	FindWorkspaceByGitDir(ctx context.Context, gitDir domain.LocalPath) (ResolvedProject, error)
	ResolveProjectWorkspace(ctx context.Context, commonDir, rootPath, gitDir domain.LocalPath) (ResolvedProject, error)
	Close() error
}
