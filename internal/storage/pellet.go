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
	Placement   *PelletPlacement
}

// PelletPlacement positions a new open pellet relative to an existing active
// pellet. Before false means after the target.
type PelletPlacement struct {
	Target domain.PelletReference
	Before bool
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

// PelletListOptions describes project-scoped exact filters and deterministic
// ordering. A nil Status selects the active queue unless All is true.
type PelletListOptions struct {
	Status     *domain.PelletStatus
	ExternalID *string
	Group      *string
	All        bool
	Limit      *int64
}

type NextSelectionReason string

const (
	NextResumeInProgress NextSelectionReason = "resume_in_progress"
	NextOpen             NextSelectionReason = "next_open"
	NextNone             NextSelectionReason = "none"
)

// NextSelection is the read-only queue choice for one resolved workspace.
type NextSelection struct {
	Reason NextSelectionReason
	Pellet *Pellet
}

// PelletRepository is the transactional persistence boundary used by pellet
// application services.
type PelletRepository interface {
	CreatePellet(ctx context.Context, project ResolvedProject, input NewPellet) (Pellet, error)
	ListPellets(ctx context.Context, project ResolvedProject, options PelletListOptions) ([]Pellet, error)
	NextPellet(ctx context.Context, project ResolvedProject, externalID, group *string) (NextSelection, error)
	ReadPellet(ctx context.Context, project ResolvedProject, reference domain.PelletReference) (Pellet, error)
	UpdatePellet(ctx context.Context, project ResolvedProject, reference domain.PelletReference, changes PelletChanges) (Pellet, error)
	Close() error
}
