package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CheckpointTriage retains completed assessments across attempts. An absent
// assessment is unfinished work, never an ignored finding or a clean review.
type CheckpointTriage struct {
	ReviewThreadID string              `json:"review_thread_id"`
	ReviewTurnID   string              `json:"review_turn_id"`
	Assessments    []FindingAssessment `json:"assessments"`
}

type FindingAssessment struct {
	FindingID      string `json:"finding_id"`
	Decision       string `json:"decision"`
	Reason         string `json:"reason"`
	Title          string `json:"title,omitempty"`
	Context        string `json:"context,omitempty"`
	Acceptance     string `json:"acceptance,omitempty"`
	DuplicateOf    string `json:"duplicate_of,omitempty"`
	ExistingNumber int64  `json:"existing_number,omitempty"`
	PelletNumber   int64  `json:"pellet_number,omitempty"`
	ThreadID       string `json:"thread_id"`
	TurnID         string `json:"turn_id"`
}

// Finding IDs survive request-receipt expiration and repeated identical native
// comments. Semantic duplicates are explicitly assessed against earlier IDs.
func ReviewFindingID(f ReviewFinding) string {
	f.Priority = 0
	f.Title = strings.Join(strings.Fields(f.Title), " ")
	f.Body = strings.Join(strings.Fields(f.Body), " ")
	b, _ := json.Marshal(f)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func ValidateFindingAssessment(a FindingAssessment) error {
	text := a.Title + a.Context + a.Acceptance + a.Reason
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return InvalidExecutionRun("triage assessment must contain valid UTF-8 text without NUL bytes")
	}
	if !hexDigest.MatchString(a.FindingID) || strings.TrimSpace(a.Reason) == "" || !safeRunID.MatchString(a.ThreadID) || !safeRunID.MatchString(a.TurnID) || a.ThreadID == "" || a.TurnID == "" || len(a.ThreadID) > 256 || len(a.TurnID) > 256 {
		return InvalidExecutionRun("triage requires a finding identity, evidence-based reason, and exact conversation IDs")
	}
	switch a.Decision {
	case "valid":
		if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Context) == "" || strings.TrimSpace(a.Acceptance) == "" {
			return InvalidExecutionRun("valid findings require actionable context and acceptance criteria")
		}
	case "duplicate":
		if !hexDigest.MatchString(a.DuplicateOf) || a.DuplicateOf == a.FindingID {
			return InvalidExecutionRun("duplicates must identify an earlier distinct finding")
		}
	case "existing":
		if a.ExistingNumber < 1 {
			return InvalidExecutionRun("existing issues require an exact open or in-progress Pellet")
		}
	case "invalid", "already_fixed", "stylistic":
	default:
		return InvalidExecutionRun("unknown triage disposition")
	}
	if a.Decision != "duplicate" && a.DuplicateOf != "" || a.Decision != "existing" && a.ExistingNumber != 0 {
		return InvalidExecutionRun("triage disposition has unrelated references")
	}
	b, err := json.Marshal(a)
	if err != nil || len(b) > MaxRunSnapshotBytes {
		return InvalidExecutionRun("triage assessment exceeds its storage bound")
	}
	return nil
}

type CheckpointTriageDatabase interface {
	BeginCheckpointTriage(context.Context, int64, int64) (*CheckpointTriage, error)
	ReadCheckpointTriage(context.Context, int64) (*CheckpointTriage, error)
	CheckpointTriageQueue(context.Context, int64) ([]Pellet, string, error)
	ReconcileCheckpointFinding(context.Context, int64, FindingAssessment, string) (FindingAssessment, error)
}
