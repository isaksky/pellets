package storage

import (
	"context"
	"time"

	"pellets/internal/domain"
)

// Memory is one immutable project-scoped knowledge record. It deliberately
// carries no pellet identity, lifecycle, ordering, external-ID, or group data.
type Memory struct {
	ID          int64
	ProjectID   int64
	ProjectCode string
	Text        string
	CreatedBy   domain.MemoryCreator
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ApprovedAt  *time.Time
}

// NewMemory contains all user-controlled fields accepted at creation.
type NewMemory struct {
	Text      string
	CreatedBy domain.MemoryCreator
}

// MemoryListOptions describes the only v1 list filters.
type MemoryListOptions struct {
	ApprovedOnly bool
	Limit        *int64
}

// MemoryRepository is the persistence boundary for creation, retrieval, and
// human approval of project memory.
type MemoryRepository interface {
	CreateMemory(ctx context.Context, project Project, input NewMemory) (Memory, error)
	ListMemories(ctx context.Context, project Project, options MemoryListOptions) ([]Memory, error)
	ReadMemory(ctx context.Context, project Project, memoryID int64) (Memory, error)
	ApproveMemory(ctx context.Context, project Project, memoryID int64) (Memory, error)
	Close() error
}
