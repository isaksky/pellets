package storage

import (
	"context"
	"time"

	"pellets/internal/domain"
)

// Pellet is one authoritative queue record. Workspace is non-nil exactly
// while Status is in_progress.
type Pellet struct {
	ProjectID   int64
	Reference   domain.PelletReference
	Title       string
	Description string
	ExternalID  *string
	Group       *string
	Status      domain.PelletStatus
	Priority    *int64
	Workspace   *Workspace
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
}

// NewPellet contains the fields accepted when allocating a new pellet. Status
// may be open or maybe_later; an empty status means open.
type NewPellet struct {
	Title       string
	Description string
	ExternalID  *string
	Group       *string
	Status      domain.PelletStatus
}

// NullableTextChange distinguishes an omitted edit from setting a nullable
// field to either a string or NULL.
type NullableTextChange struct {
	Set   bool
	Value *string
}

// PelletChanges contains only editable fields. Identity, lifecycle state,
// priority, and workspace ownership are deliberately absent.
type PelletChanges struct {
	Title       *string
	Description *string
	ExternalID  NullableTextChange
	Group       NullableTextChange
}

// PelletRepository is the transactional persistence boundary used by pellet
// application services.
type PelletRepository interface {
	CreatePellet(ctx context.Context, project ResolvedProject, input NewPellet) (Pellet, error)
	ReadPellet(ctx context.Context, project ResolvedProject, reference domain.PelletReference) (Pellet, error)
	UpdatePellet(ctx context.Context, project ResolvedProject, reference domain.PelletReference, changes PelletChanges) (Pellet, error)
	Close() error
}
