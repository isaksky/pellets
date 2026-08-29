// Package storage defines the persistence boundaries consumed by application
// services. Concrete SQL remains in storage/sqlite.
package storage

import (
	"context"
	"time"
)

// Project is the storage representation of one registered Git work tree.
type Project struct {
	Code      string
	RootPath  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProjectDatabase is the project persistence boundary for one selected
// database. Registration returns created=false for an idempotent repeat.
type ProjectDatabase interface {
	RegisterProject(ctx context.Context, code, rootPath string) (project Project, created bool, err error)
	ListProjects(ctx context.Context) ([]Project, error)
	FindProjectByCode(ctx context.Context, code string) (Project, error)
	FindProjectByRootPath(ctx context.Context, rootPath string) (Project, error)
	Close() error
}
