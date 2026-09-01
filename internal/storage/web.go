package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	"pellets/internal/domain"
)

// WebExactFilter distinguishes an omitted exact filter from an exact NULL
// filter. It is used for opaque group values, including ungrouped pellets.
type WebExactFilter struct {
	Set   bool
	Value *string
}

// WebPelletSortColumn names one sortable task-table value. These strings are
// also the stable URL values used by the local inspector.
type WebPelletSortColumn string

const (
	WebPelletSortReference  WebPelletSortColumn = "reference"
	WebPelletSortTitle      WebPelletSortColumn = "title"
	WebPelletSortGroup      WebPelletSortColumn = "group"
	WebPelletSortStatus     WebPelletSortColumn = "status"
	WebPelletSortPriority   WebPelletSortColumn = "priority"
	WebPelletSortExternalID WebPelletSortColumn = "external_id"
	WebPelletSortUpdated    WebPelletSortColumn = "updated"
)

// WebPelletSortDirection is deliberately limited to SQL-independent URL
// values. The SQLite implementation maps these values to fixed ORDER BY
// clauses and never interpolates request input.
type WebPelletSortDirection string

const (
	WebPelletSortAscending  WebPelletSortDirection = "asc"
	WebPelletSortDescending WebPelletSortDirection = "desc"
)

// WebPelletSort is the normalized ordering state for the task table.
type WebPelletSort struct {
	Column    WebPelletSortColumn
	Direction WebPelletSortDirection
}

// NormalizeWebPelletSort gives absent or invalid components deterministic,
// safe defaults while retaining a valid component supplied alongside one.
func NormalizeWebPelletSort(sort WebPelletSort) WebPelletSort {
	switch sort.Column {
	case WebPelletSortReference, WebPelletSortTitle, WebPelletSortGroup,
		WebPelletSortStatus, WebPelletSortPriority, WebPelletSortExternalID,
		WebPelletSortUpdated:
	default:
		sort.Column = WebPelletSortPriority
	}
	switch sort.Direction {
	case WebPelletSortAscending, WebPelletSortDescending:
	default:
		sort.Direction = WebPelletSortAscending
	}
	return sort
}

// WebPelletFilters are the composable, project-scoped filters exposed by the
// local inspector. Empty Status means every lifecycle state.
type WebPelletFilters struct {
	Status     *domain.PelletStatus
	ExternalID *string
	Group      WebExactFilter
	Query      string
	Sort       WebPelletSort
}

// WebProjectSummary combines one authoritative project with materialized
// counts. Counts are invalidation hints for navigation, never stored state.
type WebProjectSummary struct {
	Project        Project
	Open           int64
	InProgress     int64
	Closed         int64
	MaybeLater     int64
	MemoryCount    int64
	ApprovedMemory int64
}

// WebReader is the query-only storage boundary. Every method materializes its
// complete result and closes SQLite rows before it returns.
type WebReader interface {
	ListWebProjects(ctx context.Context) ([]WebProjectSummary, error)
	ListWebPellets(ctx context.Context, project Project, filters WebPelletFilters) ([]Pellet, error)
	ReadWebPellet(ctx context.Context, project Project, reference domain.PelletReference) (Pellet, error)
	ListWebGroups(ctx context.Context, project Project) ([]*string, error)
	ListWebMemories(ctx context.Context, project Project) ([]Memory, error)
	ReadWebMemory(ctx context.Context, project Project, memoryID int64) (Memory, error)
	Close() error
}

// WebWriter is the version-checked mutation boundary used by the web
// application service. Implementations validate ExpectedVersion while holding
// the same short writer transaction that performs the mutation.
type WebWriter interface {
	CreateWebPellet(ctx context.Context, project Project, input NewPellet) (Pellet, error)
	UpdateWebPellet(ctx context.Context, project Project, reference domain.PelletReference, expectedVersion string, changes PelletChanges) (Pellet, error)
	MoveWebPellet(ctx context.Context, project Project, reference domain.PelletReference, expectedVersion string, placement PelletPlacement) (Pellet, error)
	TransitionWebPellet(ctx context.Context, project ResolvedProject, reference domain.PelletReference, expectedVersion string, request PelletLifecycleRequest) (PelletLifecycleResult, error)
	CreateWebMemory(ctx context.Context, project Project, input NewMemory) (Memory, error)
	UpdateWebMemory(ctx context.Context, project Project, memoryID int64, expectedVersion, text string) (Memory, error)
	ApproveWebMemory(ctx context.Context, project Project, memoryID int64, expectedVersion string) (Memory, error)
	Close() error
}

// OptimisticConflict carries the authoritative row observed under the writer
// lock. HTTP presentation can therefore show it beside the preserved draft
// without holding a database connection while rendering.
type OptimisticConflict struct {
	Pellet *Pellet
	Memory *Memory
}

func (conflict *OptimisticConflict) Error() string {
	return "the record changed after this editor was opened"
}

// PelletVersion and MemoryVersion are opaque digests of the complete
// authoritative row state. JSON provides stable, length-delimited encoding;
// the type definitions make field additions participate automatically.
func PelletVersion(pellet Pellet) string { return rowVersion(pellet) }
func MemoryVersion(memory Memory) string { return rowVersion(memory) }

func rowVersion(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("storage row version encoding failed: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
