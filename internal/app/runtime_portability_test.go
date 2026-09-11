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
				r.ResumeFrom = &run.ID
				r.ResumePellet = &run.PelletNumber
				resumed := awaitSchedule(t, startSchedule(t, s, r))
				if resumed.State != "completed" {
					t.Fatalf("resume failed: %+v", resumed)
				}
				history, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, run.ID)
				if err != nil || history.Summary != run.Summary || history.Settings.Codex.Executable != "/missing-old-machine/codex" {
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
