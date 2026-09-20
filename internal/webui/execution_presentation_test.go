package webui

import (
	"testing"

	"pellets/internal/storage"
)

func TestExecutionPresentationUsesAuthoritativeState(t *testing.T) {
	for _, tc := range []struct {
		name, state, outcome, pending, label string
		interaction                          *storage.RunInteraction
		working                              bool
	}{
		{name: "completed activity is not completion", state: "running", label: "Working", working: true},
		{name: "input", state: "awaiting_input", label: "Waiting for input"},
		{name: "approval", state: "awaiting_input", label: "Waiting for approval", interaction: &storage.RunInteraction{Method: "item/commandExecution/requestApproval"}},
		{name: "stop now intent", state: "running", pending: "turn/interrupt", label: "Stopping"},
		{name: "attention", state: "needs_attention", outcome: "unknown", label: "Needs attention"},
		{name: "failed", state: "needs_attention", outcome: "failed", label: "Failed · needs attention"},
		{name: "interrupted", state: "interrupted", outcome: "cancelled", label: "Interrupted"},
		{name: "finished", state: "completed", outcome: "succeeded", label: "Finished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := makeRunView(storage.ExecutionRun{PendingOperation: tc.pending, RunProgress: storage.RunProgress{
				State: tc.state, Phase: "implementation", Outcome: tc.outcome, Interaction: tc.interaction,
				Summary: "Codex completed an activity; validating the bound result.",
			}})
			workspace := runWorkspaceView{Run: &run}
			got := workspace.ExecutionStatus()
			if got.Label != tc.label || got.Working != tc.working || got.ShowOperation != tc.working {
				t.Fatalf("presentation = %+v", got)
			}
			workspace.Schedule = &scheduleView{State: "running", StopAfter: true}
			if after := workspace.ExecutionStatus(); *after != *got {
				t.Fatalf("stop-after replaced current work/wait state: %+v", after)
			}
		})
	}
}

func TestExecutionPhasesAndRecovery(t *testing.T) {
	stopping := runWorkspaceView{Run: &runView{State: "running", Active: true}, Schedule: &scheduleView{Stopping: true}}
	if got := stopping.ExecutionStatus(); got.Label != "Stopping" || got.Working || got.ShowOperation {
		t.Fatalf("stop acknowledgement lost stopping state during cleanup: %+v", got)
	}
	for phase, want := range map[string]string{
		"preflight": "Checking the workspace and runtime.", "thread_start": "Preparing the agent conversation.",
		"turn_start": "Starting the agent's work.", "implementation": "Working on the pellet.",
		"verification": "Checking the implementation result.", "commit": "Saving the verified changes.",
		"close": "Closing the completed pellet.", "review": "Reviewing the selected changes and findings.",
		"finalization": "Finalizing the execution result.",
	} {
		workspace := runWorkspaceView{Run: &runView{State: "running", Phase: phase, Active: true}}
		got := workspace.ExecutionStatus()
		if got.Detail != want || !got.Working || got.ShowOperation != (phase == "implementation" || phase == "review") {
			t.Fatalf("%s: %+v", phase, got)
		}
		workspace.Run.UnownedActive = true
		if recovered := workspace.ExecutionStatus(); recovered.Working || recovered.ShowOperation || recovered.Label != "Needs attention" {
			t.Fatalf("unowned active run looks live: %+v", recovered)
		}
	}
	for _, workspace := range []runWorkspaceView{
		{RecoveryAttention: "cleanup unconfirmed"}, {NoRunResume: &noRunResumeView{}},
		{Schedule: &scheduleView{State: "waiting"}},
	} {
		if got := workspace.ExecutionStatus(); got == nil || got.Working || got.ShowOperation {
			t.Fatalf("inactive workspace looks live: %+v", got)
		}
	}
}

func TestWaitingScheduleWithHistoricalRun(t *testing.T) {
	for _, state := range []string{"", "completed", "resolved"} {
		t.Run("history_"+state, func(t *testing.T) {
			workspace := runWorkspaceView{Schedule: &scheduleView{Mode: "watch", State: "waiting"}}
			if state != "" {
				workspace.Run = &runView{State: state, Phase: "implementation"}
			}
			want := executionPresentation{Label: "Waiting for work", Detail: "Waiting for a matching pellet."}
			if got := workspace.ExecutionStatus(); got == nil || *got != want {
				t.Fatalf("waiting schedule with %q history: %+v", state, got)
			}
			workspace.Schedule.Stopping = true
			if got := workspace.ExecutionStatus(); got.Label != "Stopping" || got.Working || got.ShowOperation {
				t.Fatalf("stop intent hidden by waiting schedule: %+v", got)
			}
			workspace.RecoveryAttention = "cleanup unconfirmed"
			if got := workspace.ExecutionStatus(); got.Label != "Needs attention" || got.Working || got.ShowOperation {
				t.Fatalf("recovery hidden by stopping schedule: %+v", got)
			}
			workspace.RecoveryAttention, workspace.Schedule = "", nil
			if got := workspace.ExecutionStatus(); state == "" {
				if got != nil {
					t.Fatalf("stopped schedule without history: %+v", got)
				}
			} else if want := map[string]string{"completed": "Finished", "resolved": "Ended"}[state]; got.Label != want || got.Working || got.ShowOperation {
				t.Fatalf("stopped schedule lost historical state: %+v", got)
			}
		})
	}
	// Run capture and schedule updates can be observed independently. Waiting
	// must not hide active work, a human wait, or recovery of an unfinished run.
	for _, workspace := range []runWorkspaceView{
		{Run: &runView{State: "running", Active: true, Phase: "implementation"}},
		{Run: &runView{State: "awaiting_input", Active: true}},
		{Run: &runView{State: "running", Active: true, PendingOperation: "turn/interrupt"}},
		{Run: &runView{State: "running", UnownedActive: true}},
		{Run: &runView{State: "needs_attention", Outcome: "failed"}},
		{Run: &runView{State: "interrupted"}},
		{Run: &runView{State: "completed"}, RecoveryAttention: "cleanup unconfirmed"},
		{Run: &runView{State: "completed"}, NoRunResume: &noRunResumeView{}},
	} {
		want := *workspace.ExecutionStatus()
		workspace.Schedule = &scheduleView{Mode: "watch", State: "waiting"}
		if got := workspace.ExecutionStatus(); *got != want {
			t.Errorf("waiting schedule changed %q to %+v", want.Label, got)
		}
	}
}
