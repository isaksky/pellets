package storage

import (
	"context"
	"encoding/json"
)

// ExecutionChange retains both exact inputs and the separate assessor's receipt.
// It is execution evidence, not a general pellet edit history.
type ExecutionChange struct {
	RunID        int64             `json:"run_id"`
	FromRevision int64             `json:"from_revision"`
	ToRevision   int64             `json:"to_revision"`
	Old          json.RawMessage   `json:"old"`
	New          json.RawMessage   `json:"new"`
	Assessment   *ChangeAssessment `json:"assessment,omitempty"`
}

type ChangeAssessment struct {
	Significant bool   `json:"significant"`
	Reason      string `json:"reason"`
	FollowUp    string `json:"follow_up"`
	ThreadID    string `json:"thread_id"`
	TurnID      string `json:"turn_id"`
}

type ExecutionChangeDatabase interface {
	PendingExecutionChange(context.Context, int64) (*ExecutionChange, error)
	AssessExecutionChange(context.Context, ExecutionChange, ChangeAssessment) error
	AdoptExecutionChange(context.Context, ExecutionChange) (ExecutionRun, error)
}
