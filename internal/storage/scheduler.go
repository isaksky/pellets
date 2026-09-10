package storage

import (
	"context"

	"pellets/internal/domain"
)

// ScheduleSelection deliberately differs from CLI start-next: an existing
// owner requires explicit exact Resume, and must still match both filters.
type ScheduleSelection struct {
	ExternalID   *string
	Group        *string
	ResumePellet *int64
	// Ready is a read-only checkpoint readiness hook, evaluated while the queue
	// writer transaction is held. False leaves the candidate untouched. It must
	// not write to this database. Checkpoint schema/policy belongs to its owner.
	Ready func(context.Context, Pellet) (bool, error)
}

const NextNotReady NextSelectionReason = "not_ready"

type SchedulerQueue interface {
	SelectScheduledPellet(context.Context, ResolvedProject, ScheduleSelection) (NextSelection, error)
	ReadPellet(context.Context, ResolvedProject, domain.PelletReference) (Pellet, error)
	Close() error
}

func MatchesSchedule(p Pellet, externalID, group *string) bool {
	return (externalID == nil || p.ExternalID != nil && *externalID == *p.ExternalID) &&
		(group == nil || p.Group != nil && *group == *p.Group)
}
