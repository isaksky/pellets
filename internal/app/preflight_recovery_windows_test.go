package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"pellets/internal/domain"
	"pellets/internal/executionlock"
)

func TestPreflightCrashRecoveryRemainsFencedOnWindows(t *testing.T) {
	supervisor, request := supervisorFixture(t, "must-not-execute")
	request.ResumePellet = &request.Capture.PelletNumber
	request.Capture.ExpectedImplementationRevision = 1
	receipt, err := capturePreflight(context.Background(), request, request.Database.Root)
	if err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(request.Database.Root, ".git")
	lock, err := executionlock.Acquire(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Record(executionlock.Owner{Database: request.Database.Path, Preflight: receipt}); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	before, err := os.ReadFile(filepath.Join(gitDir, "pellets-execution.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Start(context.Background(), request, holdingDriver(make(chan *WorkspaceExecution, 1))); domain.PublicError(err).Code != "process_cleanup_unconfirmed" {
		t.Fatalf("Windows recovery admitted: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(gitDir, "pellets-execution.lock"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("Windows receipt changed: %s %v", after, err)
	}
}
