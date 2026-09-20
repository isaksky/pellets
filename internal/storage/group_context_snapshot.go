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

// DecodeGroupContextSnapshot preserves the difference between an explicitly
// captured empty document and a missing/null document in damaged evidence.
func DecodeGroupContextSnapshot(data []byte) (snapshot GroupContextSnapshot, err error) {
	var record struct {
		Version *int            `json:"version"`
		State   *string         `json:"state"`
		Group   json.RawMessage `json:"group"`
	}
	if len(data) > MaxGroupContextSnapshotBytes || json.Unmarshal(data, &record) != nil || record.Version == nil || record.State == nil || len(record.Group) == 0 {
		return snapshot, InvalidExecutionRun("captured group context is missing required evidence or is corrupt")
	}
	if *record.State == "captured" {
		var group struct {
			Context *string `json:"context"`
		}
		if json.Unmarshal(record.Group, &group) != nil || group.Context == nil {
			return snapshot, InvalidExecutionRun("captured group document is missing; an empty document must be explicit")
		}
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, InvalidExecutionRun("captured group context is corrupt")
	}
	return snapshot, ValidateGroupContextSnapshot(snapshot)
}

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
