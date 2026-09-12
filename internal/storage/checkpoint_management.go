package storage

import (
	"context"
	"time"

	"pellets/internal/domain"
)

// CheckpointWriter is an optional web capability. Mutations check complete
// row versions and all supplied target versions within one writer transaction.
type CheckpointWriter interface {
	UpdateWebCheckpointScope(context.Context, Project, domain.PelletReference, string, []ReviewTargetVersion) (Pellet, error)
	RemoveWebCheckpoint(context.Context, Project, domain.PelletReference, string) (Pellet, error)
	RestoreWebCheckpoint(context.Context, Project, domain.PelletReference, string) (Pellet, error)
}

// CheckpointScopeHistory records the exact superseded scope and evidence.
// Execution receipts and completed review findings remain separate immutable
// records, indexed by their captured implementation generation.
type CheckpointScopeHistory struct {
	ImplementationRevision int64             `json:"implementation_revision"`
	Scope                  *ReviewCheckpoint `json:"scope"`
	CapturedAt             time.Time         `json:"captured_at"`
}

type CheckpointHistoryReader interface {
	ReadWebCheckpointHistory(context.Context, Project, domain.PelletReference) ([]CheckpointScopeHistory, error)
}
