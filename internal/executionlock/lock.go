// Package executionlock coordinates participating foreground servers by the
// canonical Git worktree directory, independently of database or project codes.
package executionlock

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unicode/utf8"

	"pellets/internal/domain"
)

// Owner is diagnostic evidence, never a PID lease or an authority to kill.
type Owner struct {
	Database  string     `json:"database"`
	RunID     int64      `json:"run_id,omitempty"`
	Preflight *Preflight `json:"preflight,omitempty"`
}

// Preflight identifies work selected before any conversation or run exists.
// These receipts are versioned; older database-only receipts cannot authorize
// a fresh conversation. They remain evidence, never process cleanup proof.
type Preflight struct {
	Version                int     `json:"version"`
	Platform               string  `json:"platform"`
	DatabaseIdentity       string  `json:"database_identity"`
	GitIdentity            string  `json:"git_identity"`
	ProjectID              int64   `json:"project_id"`
	WorkspaceID            int64   `json:"workspace_id"`
	PelletNumber           int64   `json:"pellet_number"`
	ImplementationRevision int64   `json:"implementation_revision"`
	Root                   string  `json:"root"`
	GitDir                 string  `json:"git_dir"`
	GitCommonDir           string  `json:"git_common_dir"`
	StartingHead           string  `json:"starting_head"`
	StartingRef            string  `json:"starting_ref"`
	Mode                   string  `json:"mode"`
	ScheduleMode           string  `json:"schedule_mode"`
	ScheduleRemaining      int     `json:"schedule_remaining"`
	ExternalID             *string `json:"external_id"`
	Group                  *string `json:"group"`
}

const maxReceiptBytes = 64 << 10

var preflightKeys = []string{"version", "platform", "database_identity", "git_identity", "project_id", "workspace_id", "pellet_number", "implementation_revision", "root", "git_dir", "git_common_dir", "starting_head", "starting_ref", "mode", "schedule_mode", "schedule_remaining", "external_id", "group"}

// Token binds a browser confirmation to this exact saved intent. It is a
// content identity, not authorization or evidence of process settlement.
func (preflight *Preflight) Token() string {
	data, _ := json.Marshal(preflight)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func decodeOwner(data []byte) (*Owner, error) {
	// Reject duplicate fields (including nested fields), unknown fields, and
	// trailing values; permissive JSON decoding could reinterpret a receipt.
	decoder := json.NewDecoder(bytes.NewReader(data))
	allowed := map[string]bool{"database": true, "run_id": true, "preflight": true}
	for _, key := range preflightKeys {
		allowed[key] = true
	}
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if token == json.Delim('{') {
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || !allowed[name] || seen[name] {
					return errors.New("ambiguous recovery receipt")
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
		} else if _, nested := token.(json.Delim); nested {
			return errors.New("invalid recovery receipt")
		}
		return err
	}
	if len(data) > maxReceiptBytes {
		return nil, errors.New("oversized recovery receipt")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("invalid recovery receipt encoding")
	}
	if err := value(); err != nil {
		return nil, err
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var owner Owner
	if err := decoder.Decode(&owner); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || owner.Database == "" || owner.RunID < 0 || owner.RunID != 0 && owner.Preflight != nil {
		return nil, errors.New("invalid recovery receipt")
	}
	if owner.Preflight != nil {
		var envelope map[string]json.RawMessage
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &envelope) != nil || json.Unmarshal(envelope["preflight"], &fields) != nil {
			return nil, errors.New("invalid preflight receipt")
		}
		// In particular, absent filters must not be interpreted as a captured
		// unfiltered schedule. Version 1 writes explicit nulls for those fields.
		for _, key := range preflightKeys {
			if _, present := fields[key]; !present {
				return nil, errors.New("incomplete preflight receipt")
			}
		}
	}
	return &owner, nil
}

// ReadOwner is display-only and never obtains, clears, or proves a stopped lock.
func ReadOwner(gitDir string) (*Owner, error) {
	path := filepath.Join(gitDir, "pellets-execution.lock")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("invalid recovery receipt file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("recovery receipt identity changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReceiptBytes+1))
	if err != nil || len(data) == 0 {
		return nil, err
	}
	return decodeOwner(data)
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
	data, err := io.ReadAll(io.LimitReader(file, maxReceiptBytes+1))
	if err != nil {
		lock.Close()
		return nil, err
	}
	if len(data) != 0 {
		owner, decodeErr := decodeOwner(data)
		if recovery && decodeErr == nil {
			lock.owner = owner
			return lock, nil
		}
		lock.Close()
		details := map[string]any{"lock_path": path}
		if owner != nil {
			details["database"], details["run_id"] = owner.Database, owner.RunID
		}
		return nil, domain.NewError(domain.Conflict, "workspace_execution_recovery_required", "a previous server did not finish workspace cleanup; inspect its execution evidence before explicit recovery; do not delete the lock file", details)
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
	if len(data) > maxReceiptBytes {
		return errors.New("oversized recovery receipt")
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
