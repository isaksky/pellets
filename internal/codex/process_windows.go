package codex

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A non-inheritable, unnamed job contains the app-server and its descendants.
// Starting suspended closes the spawn-before-assignment race: no runtime code
// executes until assignment succeeds. Breakaway flags are never enabled.
type processTree struct {
	job  windows.Handle
	once sync.Once
	err  error
}

func (p *processTree) refresh() error { return nil }

func startProcessTree(cmd *exec.Cmd) (_ *processTree, err error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Codex process job: %w", err)
	}
	tree := &processTree{job: job}
	defer func() {
		if err != nil {
			_ = tree.terminate()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, fmt.Errorf("configure Codex process job: %w", err)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return nil, fmt.Errorf("open suspended Codex process: %w", err)
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return nil, fmt.Errorf("contain Codex process; no uncontained runtime was started: %w", err)
	}
	if err = resumePrimaryThread(uint32(cmd.Process.Pid)); err != nil {
		return nil, fmt.Errorf("resume contained Codex process: %w", err)
	}
	return tree, nil
}

// os/exec does not expose CreateProcess's primary-thread handle. While the new
// process is suspended it has one thread; enumerate that thread using the
// documented Tool Help API and resume it only after job assignment.
func resumePrimaryThread(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return err
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	return fmt.Errorf("primary thread not found for suspended process %d", pid)
}

func (p *processTree) terminate() error {
	p.once.Do(func() {
		defer func() { p.err = errors.Join(p.err, windows.CloseHandle(p.job)) }()
		if err := windows.TerminateJobObject(p.job, 1); err != nil {
			p.err = err
			return
		}
		// Termination is asynchronous. Observe zero active processes before
		// reporting cleanup complete, with a bound for unusual OS failures.
		deadline := time.Now().Add(2 * time.Second)
		for {
			var accounting struct {
				TotalUserTime, TotalKernelTime, ThisPeriodUserTime, ThisPeriodKernelTime       int64
				TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses uint32
			}
			if err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
				p.err = err
				return
			}
			if accounting.ActiveProcesses == 0 {
				return
			}
			if time.Now().After(deadline) {
				p.err = fmt.Errorf("Codex job still has %d active processes after termination", accounting.ActiveProcesses)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return p.err
}
