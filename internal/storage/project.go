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
	Redirects    []ProjectCodeRedirect
	GitCommonDir domain.LocalPath
	Workspaces   []Workspace
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ProjectCodeRedirect reserves one former public project code and points
// directly to the stable logical project row. Redirects never target another
// redirect, so resolution is always one lookup.
type ProjectCodeRedirect struct {
	Code      string
	ProjectID int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectCodeConflict is the public, canonicalized description of a redirect
// rule whose removal would be required by a rename.
type ProjectCodeConflict struct {
	Code          string
	ProjectID     int64
	CanonicalCode string
}

// ProjectRenamePlan is a write-free snapshot used for prompting and for exact
// conflict-set revalidation inside the eventual rename transaction.
type ProjectRenamePlan struct {
	Project   Project
	NewCode   string
	Conflicts []ProjectCodeConflict
}

// ProjectRenameRequest authorizes deletion only of the exact conflicts in the
// plan shown to the caller. Storage revalidates this set while holding the
// writer lock before changing any row.
type ProjectRenameRequest struct {
	ProjectID                    int64
	NewCode                      string
	DeleteConflictingRedirects   bool
	ExpectedConflictingRedirects []ProjectCodeConflict
}

// ProjectRenameResult materializes the canonical project after the atomic
// operation and records exactly which redirect rules were removed.
type ProjectRenameResult struct {
	Project          Project
	PreviousCode     string
	RemovedConflicts []ProjectCodeConflict
	Changed          bool
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
	PlanProjectRename(ctx context.Context, projectID int64, newCode string) (ProjectRenamePlan, error)
	RenameProject(ctx context.Context, request ProjectRenameRequest) (ProjectRenameResult, error)
	FindWorkspaceByGitDir(ctx context.Context, gitDir domain.LocalPath) (ResolvedProject, error)
	ResolveProjectWorkspace(ctx context.Context, commonDir, rootPath, gitDir domain.LocalPath) (ResolvedProject, error)
	Close() error
}
