package webui

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

type checkpointReadinessView struct {
	Label, Detail string
	ScopeChanged  bool
}

// Readiness is distinct from execution or review outcome. Only unfinished
// targets are waiting for completion; changed scope needs an explicit edit.
func checkpointReadiness(checkpoint *storage.ReviewCheckpoint) checkpointReadinessView {
	if checkpoint.Ready {
		return checkpointReadinessView{Label: "Ready", Detail: "Reviews the requirements and commits captured by each selected pellet’s latest successful implementation."}
	}
	counts := map[string]int{}
	for _, target := range checkpoint.Targets {
		counts[target.Reason]++
	}
	v := checkpointReadinessView{Label: "Review not ready", ScopeChanged: counts["scope_changed"] > 0}
	var reasons []string
	for _, reason := range []string{"scope_changed", "target_missing", "evidence_missing", "target_incomplete"} {
		n := counts[reason]
		if n == 0 {
			continue
		}
		noun := "pellets"
		if n == 1 {
			noun = "pellet"
		}
		var label, detail string
		switch reason {
		case "scope_changed":
			label = "Scope changed"
			detail = fmt.Sprintf("The saved review scope is out of date for %d selected %s. Review their current versions before saving a new scope.", n, noun)
		case "target_missing":
			label = "Target unavailable"
			detail = fmt.Sprintf("Target records are unavailable for %d selected %s. Inspect the selected targets and update scope.", n, noun)
		case "evidence_missing":
			label = "Evidence missing"
			detail = fmt.Sprintf("Matching successful implementation evidence is missing for %d selected %s. Inspect their execution details.", n, noun)
		case "target_incomplete":
			label = fmt.Sprintf("Waiting for %d %s", n, noun)
			detail = fmt.Sprintf("Review requires completion of %d selected %s.", n, noun)
		}
		if len(reasons) == 0 {
			v.Label = label
		}
		reasons = append(reasons, detail)
	}
	v.Detail = strings.Join(reasons, " ")
	if v.Detail == "" {
		v.Detail = "Inspect the selected targets; their readiness could not be confirmed."
	}
	return v
}

func (p pelletView) NeedsScopeUpdate() bool {
	return p.Pellet.Checkpoint != nil && p.Pellet.Status == domain.PelletOpen && checkpointReadiness(p.Pellet.Checkpoint).ScopeChanged
}

func (p pelletView) ScopeEditURL() string {
	u, _ := url.Parse(p.URL)
	q := u.Query()
	q.Set("edit_scope", "1")
	u.RawQuery = q.Encode()
	return u.String()
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
	readiness := checkpointReadiness(p.Checkpoint)
	return readiness.Label, readiness.Detail
}
