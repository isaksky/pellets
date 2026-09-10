package executionlock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"pellets/internal/domain"
)

func TestLockFenceAndPersistentInode(t *testing.T) {
	dir := t.TempDir()
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lock.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Record(Owner{Database: "test.db", RunID: 42}); err != nil {
		t.Fatal(err)
	}
	if other, err := Acquire(dir); domain.PublicError(err).Code != "workspace_execution_busy" {
		if other != nil {
			other.Close()
		}
		t.Fatalf("duplicate lock: %v", err)
	}
	if err := lock.Clean(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(next.path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("lock replaced its inode")
	}
	if err := next.Record(Owner{Database: "test.db", RunID: 43}); err != nil {
		t.Fatal(err)
	}
	next.Close()
	if other, err := Acquire(dir); domain.PublicError(err).Code != "workspace_execution_recovery_required" {
		if other != nil {
			other.Close()
		}
		t.Fatalf("lost crash fence: %v", err)
	}
}

func TestLockRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires Windows privileges")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "pellets-execution.lock")); err != nil {
		t.Fatal(err)
	}
	if lock, err := Acquire(dir); err == nil {
		lock.Close()
		t.Fatal("accepted a redirected execution lock")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve" {
		t.Fatalf("changed unrelated file: %q %v", data, err)
	}
}

func TestRecoveryAcquisitionRetainsExactReceiptAndExclusion(t *testing.T) {
	dir := t.TempDir()
	lock, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{Database: "test.db", RunID: 42}
	if err := lock.Record(owner); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	before, err := os.ReadFile(filepath.Join(dir, "pellets-execution.lock"))
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := AcquireRecovery(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Close()
	if got := recovery.Owner(); got == nil || *got != owner {
		t.Fatalf("receipt changed: %+v", got)
	}
	if recovery.RecoveryStopped() != (runtime.GOOS != "windows") {
		t.Fatal("recovery manufactured process cleanup proof")
	}
	if other, err := AcquireRecovery(dir); domain.PublicError(err).Code != "workspace_execution_busy" {
		if other != nil {
			other.Close()
		}
		t.Fatalf("parallel recovery admitted: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "pellets-execution.lock"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("recovery erased receipt: %q %v", after, err)
	}
}
