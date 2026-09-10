package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
)

func TestCheckpointTriageRejectedFindingsDoNotCreateFollowups(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, decision := range []string{"invalid", "already_fixed", "stylistic"} {
		t.Run(decision, func(t *testing.T) {
			s, request, _, _ := preparedReviewCheckpoint(t, executable, "review_findings_"+decision)
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "completed" {
				t.Fatalf("triage: %+v", status)
			}
			run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if run.CheckpointTriage == nil || len(run.CheckpointTriage.Assessments) != 1 || run.CheckpointTriage.Assessments[0].Decision != decision || run.CheckpointTriage.Assessments[0].PelletNumber != 0 {
				t.Fatalf("ignored finding: %+v", run.CheckpointTriage)
			}
		})
	}
}

func TestCheckpointTriageFailuresKeepFindingsAndCheckpointOpen(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, test := range []struct{ mode, code string }{{"missing", "triage_result_invalid"}, {"failed", "triage_result_invalid"}, {"malformed", "triage_result_invalid"}, {"side_effect", "review_side_effect_detected"}, {"interaction", "triage_interaction_forbidden"}} {
		t.Run(test.mode, func(t *testing.T) {
			s, request, checkpoint, _ := preparedReviewCheckpoint(t, executable, "review_findings_triage_"+test.mode)
			status := awaitSchedule(t, startSchedule(t, s, request))
			if status.State != "needs_attention" || status.Reason != test.code {
				t.Fatalf("triage failure: %+v", status)
			}
			run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, status.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if run.ReviewResult == nil || len(run.ReviewResult.Findings) != 1 || run.CheckpointTriage == nil || len(run.CheckpointTriage.Assessments) != 0 {
				t.Fatalf("failure reported missing/zero findings: %+v", run)
			}
			q, err := s.options.OpenQueue(context.Background(), s.options.Database.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()
			p, err := q.ReadPellet(context.Background(), request.Selected, checkpoint.Reference)
			if err != nil || p.Status != domain.PelletInProgress {
				t.Fatalf("failure closed checkpoint: %+v %v", p, err)
			}
		})
	}
}

func TestCheckpointTriageResumePreservesPartialResultsAndOnlyAssessesUnfinished(t *testing.T) {
	s, request, checkpoint, _ := preparedReviewCheckpoint(t, installSupervisorPeer(t), "review_findings_partial")
	first := awaitSchedule(t, startSchedule(t, s, request))
	if first.State != "needs_attention" {
		t.Fatalf("partial triage: %+v", first)
	}
	run, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.CheckpointTriage == nil || len(run.CheckpointTriage.Assessments) != 1 || len(run.ReviewResult.Findings) != 2 {
		t.Fatalf("partial results: %+v", run.CheckpointTriage)
	}
	firstNumber := run.CheckpointTriage.Assessments[0].PelletNumber
	// Preflight sees changed sources, but fresh assessments in the resumed run
	// must retain the original conversation's exact captured skill/help layer.
	if err = os.WriteFile(filepath.Join(s.options.Database.Root, ".agents", "skills", "pellets", "SKILL.md"), []byte("---\nname: pellets\n---\nChanged before triage resume.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PELLETS_SUPERVISOR_PEER_VERSION", "changed-before-triage-resume")
	if err = os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("review_findings"), 0600); err != nil {
		t.Fatal(err)
	}
	resume := request
	resume.ResumeFrom, resume.ResumePellet = &run.ID, &run.PelletNumber
	second := awaitSchedule(t, startSchedule(t, s, resume))
	if second.State != "completed" {
		t.Fatalf("resumed triage: %+v error=%v", second, s.options.Supervisor.err)
	}
	completed, err := s.options.Supervisor.ReadRun(context.Background(), s.options.Database, second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.PelletNumber != checkpoint.Reference.Number || len(completed.CheckpointTriage.Assessments) != 2 || completed.CheckpointTriage.Assessments[0].PelletNumber != firstNumber || completed.CheckpointTriage.Assessments[1].PelletNumber == firstNumber || completed.ResultCommit != "" {
		t.Fatalf("resumed results: %+v", completed)
	}
	if completed.PromptPrefix != run.PromptPrefix {
		t.Fatal("triage resume replaced the captured prefix with changed preflight sources")
	}
	reviews, triageTurns := 0, 0
	findingTurns := map[string]int{}
	for _, event := range readPeerEvents(t, s.options.Database.Root) {
		if event.Method == "review/start" {
			reviews++
		}
		if event.Method == "turn/start" && strings.Contains(string(event.Params), "triage-thread-") {
			triageTurns++
			var params struct {
				ThreadID          string
				Input             []struct{ Text string }
				ApprovalsReviewer string
				SandboxPolicy     struct{ Type string }
			}
			if err := json.Unmarshal(event.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID == run.ThreadID || params.ApprovalsReviewer != "auto_review" || params.SandboxPolicy.Type != "readOnly" || len(params.Input) != 1 {
				t.Fatalf("triage context/policy changed: %+v", params)
			}
			prompt := params.Input[0].Text
			assertCapturedCheckpointPrefix(t, prompt, run.PromptPrefix.Text, "Independently triage exactly")
			_, contextJSON, found := strings.Cut(prompt, "Immutable input and current queue:\n")
			var input struct {
				FindingID string `json:"finding_id"`
			}
			if !found || json.Unmarshal([]byte(contextJSON), &input) != nil || input.FindingID == "" {
				t.Fatal("triage omitted its finding context after the prefix and role")
			}
			findingTurns[input.FindingID]++
		}
	}
	if reviews != 1 || triageTurns != 3 || findingTurns[storage.ReviewFindingID(run.ReviewResult.Findings[0])] != 1 || findingTurns[storage.ReviewFindingID(run.ReviewResult.Findings[1])] != 2 {
		t.Fatalf("repeated finished phase: reviews=%d triage=%d findings=%v", reviews, triageTurns, findingTurns)
	}
	db, err := sqlite.OpenPelletRepository(context.Background(), s.options.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	active, err := db.ListPellets(context.Background(), request.Selected, storage.PelletListOptions{})
	if err != nil || len(active) != 2 {
		t.Fatalf("duplicate/missing followups: %+v %v", active, err)
	}
}
