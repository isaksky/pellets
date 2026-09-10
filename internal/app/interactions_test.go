package app

import (
	"context"
	"math"
	"testing"

	"pellets/internal/storage"
)

func TestTypedAnswerRejectsNonFiniteNumbers(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf", "Infinity"} {
		if parsed, err := typedAnswer("number", value); err == nil || parsed != nil {
			t.Fatalf("non-finite number %q accepted as %#v, %v", value, parsed, err)
		}
	}
	parsed, err := typedAnswer("number", "1.25")
	if err != nil || parsed != 1.25 || math.IsNaN(parsed.(float64)) || math.IsInf(parsed.(float64), 0) {
		t.Fatalf("finite number rejected: %#v, %v", parsed, err)
	}
}

func TestSubmitInteractionScopesRunIDsToDatabase(t *testing.T) {
	supervisor := &ExecutionSupervisor{active: make(map[activeExecutionKey]*WorkspaceExecution)}
	for _, databasePath := range []string{"/projects/one/pellets.db", "/projects/two/pellets.db"} {
		execution := &WorkspaceExecution{
			database: Database{Path: databasePath},
			ctx:      context.Background(),
			actions:  make(chan interactionAction),
		}
		supervisor.active[activeExecutionKey{databasePath: databasePath, runID: 1}] = execution
		go func(path string) {
			action := <-execution.actions
			action.result <- interactionResult{run: storage.ExecutionRun{ID: 1, ProjectCode: path}}
		}(databasePath)
	}

	for _, databasePath := range []string{"/projects/one/pellets.db", "/projects/two/pellets.db"} {
		run, err := supervisor.SubmitInteraction(context.Background(), Database{Path: databasePath}, InteractionSubmission{RunID: 1, Revision: 1})
		if err != nil {
			t.Fatal(err)
		}
		if run.ProjectCode != databasePath {
			t.Fatalf("run #1 routed to %q instead of %q", run.ProjectCode, databasePath)
		}
	}
}
