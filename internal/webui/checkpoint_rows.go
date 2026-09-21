package webui

import (
	"fmt"
	"net/http"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

type checkpointTargetView struct {
	Reference, Title, URL, Status, Reason string
	Hidden                                bool
}

func checkpointTargetReason(target storage.ReviewTarget) string {
	switch target.Reason {
	case "ready":
		return "Implementation evidence ready"
	case "target_missing":
		return "Target record unavailable; selected scope retained"
	case "scope_changed":
		return "Selected scope changed; inspect and update the review scope"
	case "target_incomplete":
		return "Waiting for completion"
	case "evidence_missing":
		return "Matching successful implementation evidence is missing"
	default:
		return "Readiness unavailable"
	}
}

func (h *handler) prepareCheckpointRows(request *http.Request, data *pageData) error {
	identities := []storage.CheckpointIdentity{}
	indexes := []int{}
	for i, p := range data.Pellets {
		if p.Pellet.Checkpoint == nil {
			continue
		}
		identities = append(identities, storage.CheckpointIdentity{ProjectID: p.Pellet.ProjectID, Number: p.Pellet.Reference.Number, ImplementationRevision: p.Pellet.ImplementationRevision})
		indexes = append(indexes, i)
	}
	outcomes, err := h.application.CheckpointOutcomes(request.Context(), identities)
	if err != nil {
		return err
	}
	for i, outcome := range outcomes {
		p := &data.Pellets[indexes[i]]
		p.CSRF = data.CSRF
		p.CheckpointOutcome = makeCheckpointOutcomeView(outcome, data.Project.Code, request.URL.Query(), storage.WebPelletSort{Column: storage.WebPelletSortColumn(data.Filters.Sort), Direction: storage.WebPelletSortDirection(data.Filters.Direction)})
		live := h.application.Executions != nil && h.application.Executions.OwnsRun(h.application.Database, outcome.RunID)
		p.ReviewStatus, p.ReviewHelp = checkpointRowStatus(p.Pellet, p.CheckpointOutcome, live)
	}
	return nil
}

// Ownership and persisted "running" are not proof of a live review. Completed
// triage is likewise separate from successful completion of the exact attempt.
func checkpointRowStatus(p storage.Pellet, o *checkpointOutcomeView, live bool) (string, string) {
	if p.Checkpoint.Removed {
		return "Removed", "Deferred by removal. Restore this review from its actions menu."
	}
	if p.Status == domain.PelletMaybeLater {
		return "Deferred", "This review is outside the active queue."
	}
	if o.NeedsAttention {
		return "Needs attention", "The review attempt stopped or requires input. Inspect its outcome and execution before resuming."
	}
	if o.RunState == "running" && live && p.Status == domain.PelletInProgress {
		if o.ReviewCompleted {
			if o.Triage != "complete" {
				return "Checking findings", fmt.Sprintf("%d of %d distinct findings assessed; follow-ups are still being reconciled.", o.Assessed, o.Findings)
			}
			return "Finishing review", "Review and finding checks are recorded; completion is being reconciled."
		}
		if o.RunPhase != "review" {
			return "Preparing review", "An active attempt is preparing the selected implementation evidence."
		}
		return "Reviewing", "An active review is examining the exact selected implementations."
	}
	if o.ReviewCompleted && o.Triage == "complete" && o.RunState == "completed" {
		if o.Status == "clean" {
			return "Reviewed with no issues", "The review completed with no findings."
		}
		if len(o.Followups) == 1 {
			return "Reviewed with 1 follow-up", "Finding checks are complete. Existing coverage and other dispositions are listed separately."
		}
		return fmt.Sprintf("Reviewed with %d follow-ups", len(o.Followups)), "Finding checks are complete. Existing coverage and other dispositions are listed separately."
	}
	if o.RunID != 0 {
		return "Needs attention", "Active execution or complete review and triage evidence could not be confirmed. Inspect the owning workspace before resuming."
	}
	if p.Status == domain.PelletClosed {
		return "Closed without review", "Queue closure has no retained successful review evidence for this generation."
	}
	if p.Status == domain.PelletInProgress {
		return "Not running", "Claimed by a workspace, with no active review attempt. Use explicit Resume in execution."
	}
	if p.Checkpoint.Ready {
		return "Ready", "All selected pellets have matching successful implementation evidence."
	}
	waiting := 0
	for _, target := range p.Checkpoint.Targets {
		if target.Reason != "ready" {
			waiting++
		}
	}
	if waiting == 1 {
		return "Waiting for 1 pellet", "Expand the scope for each target’s readiness and blocking reason."
	}
	return fmt.Sprintf("Waiting for %d pellets", waiting), "Expand the scope for each target’s readiness and blocking reason."
}
