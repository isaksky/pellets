// Package executionlock coordinates participating foreground servers by the
// canonical Git worktree directory, independently of database or project codes.
package executionlock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"pellets/internal/domain"
)

// Owner is diagnostic evidence, never a PID lease or an authority to kill.
type Owner struct {
	Database string `json:"database"`
	RunID    int64  `json:"run_id,omitempty"`
}

type Lock struct {
	file  *os.File
	path  string
	once  sync.Once
	err   error
	owner *Owner
}

// Acquire never waits or steals. The persistent inode must never be removed:
// unlinking it would allow two independent locks for the same worktree.
func Acquire(gitDir string) (*Lock, error) {
	return acquire(gitDir, false)
}

// AcquireRecovery takes the same nonblocking OS lock but retains the recovery
// receipt for inspection. It never clears it or starts work. On Unix the
// custodian inherits this lock through process cleanup; obtaining it proves
// that no participating owner or its custodian is still active.
func AcquireRecovery(gitDir string) (*Lock, error) { return acquire(gitDir, true) }

func (lock *Lock) Owner() *Owner {
	if lock.owner == nil {
		return nil
	}
	owner := *lock.owner
	return &owner
}

func (lock *Lock) RecoveryStopped() bool { return lock.owner == nil || runtime.GOOS != "windows" }

func acquire(gitDir string, recovery bool) (*Lock, error) {
	canonical, err := filepath.EvalSymlinks(gitDir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(canonical, "pellets-execution.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace execution lock must be a regular file: %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open workspace execution lock: %w", err)
	}
	opened, statErr := file.Stat()
	current, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		file.Close()
		return nil, errors.Join(errors.New("workspace execution lock identity changed"), statErr, pathErr)
	}
	if err = tryLock(file); err != nil {
		file.Close()
		if errors.Is(err, errBusy) {
			return nil, domain.NewError(domain.Conflict, "workspace_execution_busy", "another foreground server owns this worktree; no execution was started", map[string]any{"lock_path": path})
		}
		return nil, err
	}
	lock := &Lock{file: file, path: path}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		lock.Close()
		return nil, err
	}
	if len(data) != 0 {
		var owner Owner
		valid := len(data) <= 8192 && json.Unmarshal(data, &owner) == nil && owner.Database != "" && owner.RunID >= 0
		if recovery && valid {
			lock.owner = &owner
			return lock, nil
		}
		lock.Close()
		return nil, domain.NewError(domain.Conflict, "workspace_execution_recovery_required", "a previous server did not finish workspace cleanup; inspect its execution evidence before explicit recovery; do not delete the lock file", map[string]any{"lock_path": path, "database": owner.Database, "run_id": owner.RunID})
	}
	return lock, nil
}

// File is inherited only by the owned process custodian on Unix. Closing the
// server's descriptor cannot release exclusion while that custodian cleans up.
func (lock *Lock) File() *os.File { return lock.file }

func (lock *Lock) Record(owner Owner) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	if _, err = lock.file.WriteAt(data, 0); err != nil {
		return err
	}
	if err = lock.file.Truncate(int64(len(data))); err != nil {
		return err
	}
	return lock.file.Sync()
}

// Clean is allowed only after all owned work is stopped and its final outcome
// persisted. A crash or cleanup/save failure deliberately leaves a visible fence.
func (lock *Lock) Clean() error {
	if err := lock.file.Truncate(0); err != nil {
		return err
	}
	return lock.file.Sync()
}

// Close does not explicitly unlock: inherited Unix descriptors share the lock.
func (lock *Lock) Close() error {
	lock.once.Do(func() { lock.err = lock.file.Close() })
	return lock.err
}
