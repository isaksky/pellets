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
