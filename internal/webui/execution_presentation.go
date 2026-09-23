package webui

// executionPresentation is derived only from authoritative run/workspace data.
// Reported activity may describe an operation, but never changes this state.
type executionPresentation struct {
	Label, Detail string
	Working       bool
	ShowOperation bool
}

// Navigation has a bounded trailing status slot. Keep its visible label concise;
// the full state remains its accessible name, tooltip and view-menu label.
func (state executionPresentation) NavigationLabel() string {
	switch state.Label {
	case "Waiting for work":
		return "Waiting"
	case "Waiting for input":
		return "Input needed"
	case "Waiting for approval":
		return "Approval"
	case "Preparing execution":
		return "Preparing"
	case "Needs attention":
		return "Attention"
	case "Failed · needs attention":
		return "Failed"
	default:
		return state.Label
	}
}

func (workspace runWorkspaceView) ExecutionStatus() *executionPresentation {
	run := workspace.Run
	if workspace.RecoveryAttention != "" || (run != nil && run.UnownedActive) {
		return &executionPresentation{Label: "Needs attention", Detail: "Execution needs recovery. Nothing restarts automatically."}
	}
	if workspace.Schedule != nil && workspace.Schedule.Stopping {
		return &executionPresentation{Label: "Stopping", Detail: "Waiting for the current work to stop."}
	}
	if workspace.NoRunResume != nil {
		return &executionPresentation{Label: "Not running", Detail: "Resume explicitly to continue this pellet."}
	}
	// Watch retains the last run as history while waiting for another pellet.
	// A newly captured active run or unfinished recovery still takes precedence.
	if workspace.Schedule != nil && workspace.Schedule.State == "waiting" && (run == nil || run.State == "completed" || run.State == "resolved") {
		return &executionPresentation{Label: "Waiting for work", Detail: "Waiting for a matching pellet."}
	}
	if run == nil {
		if workspace.Schedule != nil {
			return &executionPresentation{Label: "Preparing execution", Detail: "Selecting and checking the next pellet.", Working: true}
		}
		return nil
	}
	if run.Active && run.PendingOperation == "turn/interrupt" {
		return &executionPresentation{Label: "Stopping", Detail: "Waiting for the current work to stop."}
	}
	switch run.State {
	case "running":
		return &executionPresentation{Label: "Working", Detail: executionPhaseDescription(run.Phase), Working: true,
			ShowOperation: run.Phase == "implementation" || run.Phase == "review"}
	case "awaiting_input":
		if run.Interaction != nil && len(run.Interaction.Questions) == 0 {
			return &executionPresentation{Label: "Waiting for approval", Detail: "Review the pending request to continue."}
		}
		return &executionPresentation{Label: "Waiting for input", Detail: "Answer the pending question to continue."}
	case "needs_attention":
		label := "Needs attention"
		if run.Outcome == "failed" {
			label = "Failed · needs attention"
		}
		return &executionPresentation{Label: label, Detail: "Execution stopped. Review the details before resuming."}
	case "interrupted":
		return &executionPresentation{Label: "Interrupted", Detail: "Execution stopped. Resume explicitly to continue."}
	case "completed":
		return &executionPresentation{Label: "Finished", Detail: "This execution is complete."}
	case "resolved":
		return &executionPresentation{Label: "Ended", Detail: "This attempt ended; its pellet is no longer active here."}
	default:
		return &executionPresentation{Label: "State unavailable", Detail: "Check the run details before taking action."}
	}
}

func executionPhaseDescription(phase string) string {
	switch phase {
	case "preflight":
		return "Checking the workspace and runtime."
	case "thread_start":
		return "Preparing the agent conversation."
	case "turn_start":
		return "Starting the agent's work."
	case "implementation":
		return "Working on the pellet."
	case "verification":
		return "Checking the implementation result."
	case "commit":
		return "Saving the verified changes."
	case "close":
		return "Closing the completed pellet."
	case "review":
		return "Reviewing the selected changes and findings."
	case "finalization":
		return "Finalizing the execution result."
	default:
		return "Execution is in progress."
	}
}
