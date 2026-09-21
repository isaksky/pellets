package webui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestCheckpointRowSeparatesReadinessActivityAndCompletion(t *testing.T) {
	pending := storage.CheckpointOutcome{Status: "pending", Triage: "not_started"}
	running := storage.CheckpointOutcome{RunID: 7, RunState: "running", RunPhase: "review", Status: "running"}
	clean := storage.CheckpointOutcome{RunID: 7, RunState: "completed", ReviewCompleted: true, Status: "clean", Triage: "complete"}
	partial := storage.CheckpointOutcome{RunID: 7, RunState: "running", RunPhase: "review", ReviewCompleted: true, Status: "findings", Triage: "partial", Findings: 3, Assessed: 1}
	findings := clean
	findings.Status, findings.Findings, findings.Assessed = "findings", 6, 6
	findings.Dispositions = []storage.CheckpointDisposition{{Decision: "valid"}, {Decision: "existing"}, {Decision: "duplicate"}, {Decision: "already_fixed"}, {Decision: "invalid"}, {Decision: "stylistic"}}
	cases := []struct {
		name                 string
		status               domain.PelletStatus
		outcome              storage.CheckpointOutcome
		live, ready, removed bool
		want                 string
	}{
		{"ready", domain.PelletOpen, pending, false, true, false, "Ready"},
		{"waiting", domain.PelletOpen, pending, false, false, false, "Waiting for 2 pellets"},
		{"idle claim", domain.PelletInProgress, pending, false, true, false, "Not running"},
		{"live", domain.PelletInProgress, running, true, true, false, "Reviewing"},
		{"orphaned running receipt", domain.PelletInProgress, running, false, true, false, "Needs attention"},
		{"checking findings", domain.PelletInProgress, partial, true, true, false, "Checking findings"},
		{"stopped partial", domain.PelletInProgress, partial, false, true, false, "Needs attention"},
		{"successful clean", domain.PelletClosed, clean, false, true, false, "Reviewed with no issues"},
		{"distinct dispositions", domain.PelletClosed, findings, false, true, false, "Reviewed with 1 follow-up"},
		{"closed without evidence", domain.PelletClosed, pending, false, true, false, "Closed without review"},
		{"removed", domain.PelletMaybeLater, pending, false, true, true, "Removed"},
		{"deferred", domain.PelletMaybeLater, pending, false, true, false, "Deferred"},
	}
	for _, state := range []string{"interrupted", "needs_attention", "awaiting_input", "failed"} {
		o := running
		o.RunState, o.NeedsAttention = state, true
		cases = append(cases, struct {
			name                 string
			status               domain.PelletStatus
			outcome              storage.CheckpointOutcome
			live, ready, removed bool
			want                 string
		}{state, domain.PelletInProgress, o, true, true, false, "Needs attention"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := storage.Pellet{Status: tc.status, Checkpoint: &storage.ReviewCheckpoint{Ready: tc.ready, Removed: tc.removed, Targets: []storage.ReviewTarget{{Reason: "ready"}, {Reason: "scope_changed"}, {Reason: "evidence_missing"}}}}
			label, help := checkpointRowStatus(p, makeCheckpointOutcomeView(tc.outcome, "test", nil, storage.WebPelletSort{}), tc.live)
			if label != tc.want || help == "" {
				t.Fatalf("label=%q help=%q; want %q", label, help, tc.want)
			}
		})
	}
	// Recorded clean findings do not imply that final reconciliation succeeded.
	for _, state := range []string{"running", "interrupted", "completed"} {
		o := clean
		o.RunState = state
		o.NeedsAttention = state == "interrupted"
		o.Triage = "partial"
		label, _ := checkpointRowStatus(storage.Pellet{Status: domain.PelletInProgress, Checkpoint: &storage.ReviewCheckpoint{}}, makeCheckpointOutcomeView(o, "test", nil, storage.WebPelletSort{}), true)
		if strings.Contains(label, "Reviewed") {
			t.Fatalf("unfinished triage marked successful: %s", label)
		}
	}
}

func TestReviewQueueCountsAndHiddenScope(t *testing.T) {
	f, p, _, _ := workbenchFixture(t)
	ctx := context.Background()
	a := addWorkbenchPellet(t, f, p, "Selected open target", nil, domain.PelletOpen)
	b := addWorkbenchPellet(t, f, p, "Selected deferred target", nil, domain.PelletMaybeLater)
	cp, err := f.application.CreatePellet(ctx, p, storage.NewPellet{Title: "Only review", Kind: domain.PelletReviewCheckpoint, ReviewTargets: []domain.PelletReference{a.Reference, b.Reference}})
	if err != nil {
		t.Fatal(err)
	}
	check := func(query string, active, ordinary, reviews int, members bool) string {
		t.Helper()
		r := performRequest(f.handler, http.MethodGet, "/projects/project1/tasks"+query, "", nil)
		s := r.Body.String()
		composition := fmt.Sprintf("%d pellets · %d reviews", ordinary, reviews)
		if ordinary == 1 {
			composition = strings.Replace(composition, "1 pellets", "1 pellet", 1)
		}
		if reviews == 1 {
			composition = strings.Replace(composition, "1 reviews", "1 review", 1)
		}
		for _, want := range []string{fmt.Sprintf("%d active in project", active), composition, fmt.Sprintf("reviews\">%d</small>", active)} {
			if r.Code != 200 || !strings.Contains(s, want) {
				t.Fatalf("%s: missing %q in response %d", query, want, r.Code)
			}
		}
		if members {
			for _, want := range []string{"Selected open target", "Selected deferred target", "Not shown by current filters", "Waiting for completion"} {
				if !strings.Contains(s, want) {
					t.Fatalf("scope lost %q", want)
				}
			}
		}
		return s
	}
	check("", 2, 1, 1, true)
	check("?status=all", 2, 2, 1, false)
	check("?q=Only+review", 2, 0, 1, true)
	check("?q=nothing-matches", 2, 0, 0, false)
	removed, err := f.application.RemoveCheckpoint(ctx, p, cp.Reference, storage.PelletVersion(cp))
	if err != nil {
		t.Fatal(err)
	}
	check("", 1, 1, 0, false)
	check("?status=all", 1, 2, 1, false)
	if s := check("?status=maybe_later", 1, 1, 1, true); !strings.Contains(s, `class="review-status">Removed`) {
		t.Fatal("removed review mislabeled")
	}
	if _, err = f.application.RestoreCheckpoint(ctx, p, removed.Reference, storage.PelletVersion(removed)); err != nil {
		t.Fatal(err)
	}
	check("", 2, 1, 1, true)
	if _, err = f.application.TransitionPellet(ctx, p, a.Reference, storage.PelletVersion(a), storage.PelletLifecycleRequest{Operation: storage.PelletClose}); err != nil {
		t.Fatal(err)
	}
	check("", 1, 0, 1, true) // checkpoint-only active queue
}
