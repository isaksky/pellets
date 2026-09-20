package storage

import (
	"encoding/json"
	"unicode/utf8"
)

// GroupContextSnapshot is historical execution evidence, independent of both
// the schedule's exact group filter and the group's current editable document.
// A captured empty document is distinct from ungrouped and legacy executions.
type GroupContextSnapshot struct {
	Version int           `json:"version"`
	State   string        `json:"state"` // captured, ungrouped, or legacy
	Group   *GroupContext `json:"group"`
}

// Allow the worst-case JSON escaping of a 1 MiB document, plus bounded metadata.
const MaxGroupContextSnapshotBytes = 6*MaxGroupContextBytes + (32 << 10)

func ValidateGroupContextSnapshot(snapshot GroupContextSnapshot) error {
	if snapshot.Version != 1 {
		return InvalidExecutionRun("unsupported captured group context version")
	}
	switch snapshot.State {
	case "legacy", "ungrouped":
		if snapshot.Group != nil {
			return InvalidExecutionRun("absent captured group context must not contain a group")
		}
	case "captured":
		g := snapshot.Group
		if g == nil || g.ID < 1 || g.Revision < 1 || g.Name == "" || len(g.Name) > 4096 || !utf8.ValidString(g.Name) {
			return InvalidExecutionRun("captured group requires a stable ID, positive revision, and UTF-8 name of at most 4096 bytes")
		}
		if err := ValidateGroupContext(g.Context); err != nil {
			return InvalidExecutionRun("captured group context must be valid UTF-8 and at most 1 MiB; it cannot be silently truncated")
		}
	default:
		return InvalidExecutionRun("invalid captured group context state")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > MaxGroupContextSnapshotBytes {
		return InvalidExecutionRun("captured group context exceeds its encoded storage bound")
	}
	return nil
}
