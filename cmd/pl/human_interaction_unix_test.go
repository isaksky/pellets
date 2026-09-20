//go:build !windows

package main

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestCompiledHumanTerminalInteractions(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("PTY integration requires python3")
	}
	executable := buildFoundationExecutable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "testdata/cli_pty.py", executable, t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("terminal interactions: %v\n%s", err, output)
	}
}
