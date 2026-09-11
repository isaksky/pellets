package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pellets/internal/codex"
	"pellets/internal/domain"
	"pellets/internal/storage"
)

func TestRuntimeCompatibilityFailsBeforeClaim(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, r, q := schedulerFixture(t, executable, "runtime_old")
	status := awaitSchedule(t, startSchedule(t, s, r))
	if status.State != "needs_attention" || !strings.Contains(status.Detail, "0.154.0") {
		t.Fatalf("lost compatibility guidance: %+v", status)
	}
	pellet, err := q.ReadPellet(context.Background(), r.Selected, domain.PelletReference{ProjectCode: r.Selected.Project.Code, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if pellet.Status != domain.PelletOpen {
		t.Fatalf("preflight claimed pellet: %s", pellet.Status)
	}
	runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(context.Background(), s.options.Database, r.Selected.Workspace.ID, 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("invented run: %+v %v", runs, err)
	}
	if _, err := os.Stat(filepath.Join(s.options.Database.Root, "fake-events.jsonl")); !os.IsNotExist(err) {
		t.Fatal("incompatible runtime started app-server")
	}
}
func TestCodexRuntimeErrorsPersistAndResume(t *testing.T) {
	executable := installSupervisorPeer(t)
	for _, mode := range []string{"schedule_runtime_error", "schedule_rpc_error"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := schedulerFixture(t, executable, mode)
			status := awaitSchedule(t, startSchedule(t, s, r))
			if status.State != "needs_attention" {
				t.Fatal(status)
			}
			run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, status.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(run.Summary, "requires a newer Codex version") || !strings.Contains(run.Summary, "[redacted]") {
				t.Fatalf("lost message: %+v", run.RunProgress)
			}
			for _, secret := range []string{"private-runtime-token", "sk-private", "private-error-details", "private-rpc-data", "\n"} {
				if strings.Contains(run.Summary, secret) {
					t.Fatalf("leaked %q", secret)
				}
			}
			if run.Settings.Runtime.Version != "codex-cli 0.154.0" || run.Settings.Runtime.Executable == "" {
				t.Fatalf("missing local runtime evidence: %+v", run.Settings)
			}
			if mode == "schedule_runtime_error" {
				// Simulate an authentic legacy snapshot: only an old machine's
				// resolved path, without the new runtime provenance object.
				encoded, err := json.Marshal(run.Settings)
				if err != nil {
					t.Fatal(err)
				}
				var legacy map[string]any
				if err = json.Unmarshal(encoded, &legacy); err != nil {
					t.Fatal(err)
				}
				delete(legacy, "runtime")
				legacy["codex"].(map[string]any)["executable"] = "/missing-old-machine/codex"
				encoded, err = json.Marshal(legacy)
				if err != nil {
					t.Fatal(err)
				}
				db, err := sql.Open("sqlite", s.options.Database.Path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("UPDATE execution_runs SET settings_json=? WHERE run_id=?", string(encoded), run.ID)
				closeErr := db.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("legacy fixture: %v %v", err, closeErr)
				}
				if err := os.WriteFile(filepath.Join(s.options.Database.Root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
					t.Fatal(err)
				}
				// The motivating failure: repair the runtime in a real commit,
				// then Resume the still-owned pellet without undoing that fix.
				if err := os.WriteFile(filepath.Join(s.options.Database.Root, "runtime-repair.txt"), []byte("keep this repair"), 0600); err != nil {
					t.Fatal(err)
				}
				gitForExecutionTest(t, s.options.Database.Root, "add", "runtime-repair.txt")
				gitForExecutionTest(t, s.options.Database.Root, "commit", "-m", "repair runtime")
				repairHead := gitForExecutionTest(t, s.options.Database.Root, "rev-parse", "HEAD")
				r.ResumeFrom = &run.ID
				r.ResumePellet = &run.PelletNumber
				resumed := awaitSchedule(t, startSchedule(t, s, r))
				if resumed.State != "completed" {
					t.Fatalf("resume failed: %+v", resumed)
				}
				current, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, resumed.RunID)
				if err != nil || current.StartingHead != repairHead || current.ThreadID != run.ThreadID || current.ResumeFrom == nil || *current.ResumeFrom != run.ID {
					t.Fatalf("resume lost baseline/conversation: %+v %v", current, err)
				}
				events, err := os.ReadFile(filepath.Join(s.options.Database.Root, "fake-events.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(events), "Commits landed since your previous attempt") {
					t.Fatal("agent was not told to reassess changed code")
				}
				history, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, run.ID)
				if err != nil || history.Summary != run.Summary || history.StartingHead != run.StartingHead || history.Settings.Codex.Executable != "/missing-old-machine/codex" {
					t.Fatal("resume erased original failure")
				}
			}
		})
	}
}
func TestCodexDiagnosticIdentityAndAllowlist(t *testing.T) {
	event := codex.Event{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"failed","error":{"message":"Upgrade Codex. token=private","additionalDetails":"do not copy"}}}`)}
	if got := codexTurnDiagnostic(&event, "thread", "turn"); got != "Codex: Upgrade Codex. [redacted]" {
		t.Fatal(got)
	}
	if codexTurnDiagnostic(&event, "other", "turn") != "" || codexTurnDiagnostic(&event, "thread", "other") != "" {
		t.Fatal("unrelated error accepted")
	}
}

func TestResumeRechecksChangedBaselineAfterPreflight(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_runtime_error")
	failed := awaitSchedule(t, startSchedule(t, s, request))
	old, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, failed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	gitForExecutionTest(t, s.options.Database.Root, "commit", "--allow-empty", "-m", "repair")
	prepare := s.options.Supervisor.options.Prepare
	s.options.Supervisor.options.Prepare = func(ctx context.Context, options codex.PrepareOptions) (*codex.PreparedRun, error) {
		prepared, err := prepare(ctx, options)
		if err == nil {
			err = os.WriteFile(filepath.Join(s.options.Database.Root, "keep-uncommitted.txt"), []byte("preserve me"), 0600)
		}
		return prepared, err
	}
	request.ResumeFrom, request.ResumePellet = &old.ID, &old.PelletNumber
	stopped := awaitSchedule(t, startSchedule(t, s, request))
	if stopped.Reason != "resume_worktree_dirty" {
		t.Fatalf("late dirt was accepted: %+v", stopped)
	}
	runs, err := s.options.Supervisor.options.Recorder.ListWorkspaceRuns(context.Background(), s.options.Database, old.WorkspaceID, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != old.ID {
		t.Fatalf("captured invalid attempt: %+v %v", runs, err)
	}
	data, err := os.ReadFile(filepath.Join(s.options.Database.Root, "keep-uncommitted.txt"))
	if err != nil || string(data) != "preserve me" {
		t.Fatal("lost uncommitted work")
	}
}

func TestResumeBaselinePolicyPreservesCommitAndReviewEvidence(t *testing.T) {
	for _, phase := range []string{"commit", "close", "finalization", "review"} {
		if storage.ResumeUsesCurrentHead(storage.ExecutionRun{RunProgress: storage.RunProgress{Phase: phase}}) {
			t.Fatalf("rebased legacy %s evidence", phase)
		}
	}
	if storage.ResumeUsesCurrentHead(storage.ExecutionRun{RunProgress: storage.RunProgress{Phase: "implementation", Finalization: &storage.FinalizationEvidence{}}}) {
		t.Fatal("rebased finalization")
	}
}

func TestImplementationAttentionPreservesReason(t *testing.T) {
	s, request, _ := schedulerFixture(t, installSupervisorPeer(t), "schedule_unfinished")
	status := awaitSchedule(t, startSchedule(t, s, request))
	if status.State != "needs_attention" || status.Reason != "implementation_needs_attention" {
		t.Fatalf("unexpected outcome: %+v", status)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, status.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.Summary, "browser test failed on a hidden table cell") || !strings.Contains(run.Summary, "[redacted]") || strings.Contains(run.Summary, "private-attention-token") {
		t.Fatalf("lost or unsafe reason: %q", run.Summary)
	}
	if run.ResultCommit != "" || run.Finalization != nil {
		t.Fatal("unfinished work was finalized")
	}
}
