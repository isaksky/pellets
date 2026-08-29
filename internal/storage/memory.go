package storage

import (
	"context"
	"time"

	"pellets/internal/domain"
)

// Memory is one project-scoped knowledge record. Its text and approval state
// are editable; it deliberately carries no pellet identity, lifecycle,
// ordering, external-ID, or group data.
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

// MemorySearchOptions describes project-scoped full-text search plus the
// relational human-approval filter.
type MemorySearchOptions struct {
	Query        string
	ApprovedOnly bool
	Limit        *int64
}

// MemorySearchResult combines one authoritative memory with disposable FTS
// ranking and snippet data. The full text remains available on Memory.
type MemorySearchResult struct {
	Memory  Memory
	Rank    float64
	Snippet string
}

// MemoryRepository is the persistence boundary for creation, retrieval, and
// human approval of project memory.
type MemoryRepository interface {
	CreateMemory(ctx context.Context, project Project, input NewMemory) (Memory, error)
	ListMemories(ctx context.Context, project Project, options MemoryListOptions) ([]Memory, error)
	SearchMemories(ctx context.Context, project Project, options MemorySearchOptions) ([]MemorySearchResult, error)
	ReadMemory(ctx context.Context, project Project, memoryID int64) (Memory, error)
	ApproveMemory(ctx context.Context, project Project, memoryID int64) (Memory, error)
	UpdateMemory(ctx context.Context, project Project, memoryID int64, text string) (Memory, error)
	RemoveMemory(ctx context.Context, project Project, memoryID int64) (Memory, error)
	RebuildMemorySearchIndex(ctx context.Context) error
	Close() error
}
