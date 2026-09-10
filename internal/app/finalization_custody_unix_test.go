//go:build darwin || linux

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"pellets/internal/domain"
	"pellets/internal/executionlock"
	"pellets/internal/storage"
	"pellets/internal/storage/sqlite"
	"pellets/internal/testcodex"
)

func TestFinalizationDelayedHookHelper(t *testing.T) {
	role := os.Getenv("PELLETS_FINALIZATION_HOOK_ROLE")
	if role == "" {
		return
	}
	if err := os.WriteFile("fake-hook-"+role+"-pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if role == "parent" {
		fmt.Fprintln(os.Stderr, "DELAYED-HOOK cancellation diagnostic")
		child := exec.Command(os.Args[0], "-test.run=^TestFinalizationDelayedHookHelper$")
		child.Env = append(os.Environ(), "PELLETS_FINALIZATION_HOOK_ROLE=child")
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("fake-hook-release"); err == nil {
			if err := os.WriteFile("escaped-hook-mutation.txt", []byte("must never happen"), 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func installDelayedFinalizationHook(t *testing.T, root string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nPELLETS_FINALIZATION_HOOK_ROLE=parent exec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestFinalizationDelayedHookHelper$\n"
	if err := os.WriteFile(filepath.Join(root, ".git", "hooks", "pre-commit"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
}

func awaitFinalizationHook(t *testing.T, root string) []int {
	t.Helper()
	pids := []int{}
	for _, role := range []string{"parent", "child"} {
		deadline := time.Now().Add(15 * time.Second)
		for {
			data, err := os.ReadFile(filepath.Join(root, "fake-hook-"+role+"-pid"))
			if err == nil {
				pid, err := strconv.Atoi(string(data))
				if err != nil {
					t.Fatal(err)
				}
				pids = append(pids, pid)
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("delayed hook did not start")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	return pids
}

func assertFinalizationHookStopped(t *testing.T, root string, pids []int) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for _, pid := range pids {
		for testcodex.Alive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("owned hook process %d survived", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "fake-hook-release"), []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped-hook-mutation.txt")); !os.IsNotExist(err) {
		t.Fatalf("hook mutated repository after stop: %v", err)
	}
}

func TestFinalizationStopNowContainsDetachedHookAndPreservesUnrelatedProcess(t *testing.T) {
	executable := installSupervisorPeer(t)
	s, request, _ := schedulerFixture(t, executable, "schedule_success")
	root := s.options.Database.Root
	installDelayedFinalizationHook(t, root)
	unrelated := exec.Command("sleep", "60")
	if err := unrelated.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
	h := startSchedule(t, s, request)
	pids := awaitFinalizationHook(t, root)
	if lock, err := executionlock.Acquire(filepath.Join(root, ".git")); domain.PublicError(err).Code != "workspace_execution_busy" {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("live hook lost exclusion: %v", err)
	}
	h.StopNow()
	status := awaitSchedule(t, h)
	if status.State != "stopped" {
		t.Fatalf("stop = %+v", status)
	}
	run, err := s.options.Supervisor.options.Recorder.Read(context.Background(), s.options.Database, status.RunID)
	if err != nil || !strings.Contains(run.Summary, "DELAYED-HOOK cancellation diagnostic") {
		t.Fatalf("interruption erased Git diagnostic: %q %v", run.Summary, err)
	}
	assertFinalizationHookStopped(t, root, pids)
	if !testcodex.Alive(unrelated.Process.Pid) {
		t.Fatal("unrelated process was terminated")
	}
	lock, err := executionlock.Acquire(filepath.Join(root, ".git"))
	if err != nil {
		t.Fatalf("confirmed Stop now did not release lock: %v", err)
	}
	lock.Close()
}

func TestFinalizationServerCrashHelper(t *testing.T) {
	encoded := os.Getenv("PELLETS_FINALIZATION_CRASH_REQUEST")
	if encoded == "" {
		return
	}
	var request ExecutionRequest
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		t.Fatal(err)
	}
	supervisor := NewExecutionSupervisor(context.Background(), SupervisorOptions{
		Recorder: ExecutionRecorder{Open: func(ctx context.Context, path string) (storage.ExecutionRunDatabase, error) {
			return sqlite.OpenExecutionRunDatabase(ctx, path)
		}},
		Settings: WorkspaceRunSettingsManager{Open: func(ctx context.Context, path string) (storage.WorkspaceRunSettingsDatabase, error) {
			return sqlite.OpenWorkspaceRunSettingsDatabase(ctx, path)
		}},
	})
	s := &Scheduler{options: SchedulerOptions{Database: request.Database, Supervisor: supervisor, OpenQueue: func(ctx context.Context, path string) (storage.SchedulerQueue, error) {
		return sqlite.OpenPelletRepository(ctx, path)
	}}}
	handle, err := supervisor.Start(context.Background(), request, s.drive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizationAbruptServerDeathContainsDetachedHook(t *testing.T) {
	executable := installSupervisorPeer(t)
	_, request := supervisorFixture(t, executable)
	root := request.Database.Root
	if err := os.WriteFile(filepath.Join(root, "fake-mode"), []byte("schedule_success"), 0600); err != nil {
		t.Fatal(err)
	}
	installDelayedFinalizationHook(t, root)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	server := exec.Command(executable, "-test.run=^TestFinalizationServerCrashHelper$")
	server.Env = append(os.Environ(), "PELLETS_FINALIZATION_CRASH_REQUEST="+string(encoded))
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Process.Kill()
	pids := awaitFinalizationHook(t, root)
	if err := server.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = server.Wait()
	assertFinalizationHookStopped(t, root, pids)
	deadline := time.Now().Add(8 * time.Second)
	for {
		lock, err := executionlock.Acquire(filepath.Join(root, ".git"))
		if lock != nil {
			lock.Close()
			t.Fatal("server death silently cleared recovery fence")
		}
		code := domain.PublicError(err).Code
		if code == "workspace_execution_recovery_required" {
			break
		}
		if code != "workspace_execution_busy" || time.Now().After(deadline) {
			t.Fatal(fmt.Sprintf("custody did not settle: %v", err))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
