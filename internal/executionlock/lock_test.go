package executionlock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestRecoveryRejectsUnicodeFieldAliases(t *testing.T) {
	owner := Owner{Database: "test.db", Preflight: &Preflight{Version: 1, ScheduleRemaining: 1}}
	encoded, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"ſchedule_remaining", `\u017fchedule_remaining`, "SCHEDULE_REMAINING"} {
		data := strings.Replace(string(encoded), `"schedule_remaining":1`, `"schedule_remaining":1,"`+alias+`":99`, 1)
		if _, err := decodeOwner([]byte(data)); err == nil {
			t.Fatalf("accepted noncanonical alias %q", alias)
		}
	}
	if _, err := decodeOwner(encoded); err != nil {
		t.Fatalf("rejected canonical ASCII receipt: %v", err)
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

func TestRecoveryRejectsAmbiguousAndUnknownReceiptFields(t *testing.T) {
	for _, data := range []string{
		`{"database":"a","database":"b"}`,
		`{"database":"a","Database":"b"}`,
		`{"database":"a","run_id":0,"run_id":1}`,
		`{"database":"a","unknown":1}`,
		`{"database":"a","preflight":{"version":1,"version":2}}`,
		`{"database":"a","preflight":{"unexpected":1}}`,
		`{"database":"a","preflight":{"version":1}}`,
		`{"database":"a"} {"database":"b"}`,
		`{"database":"a","run_id":2,"preflight":{"version":1}}`,
		"{\"database\":\"\xff\"}",
	} {
		t.Run(data, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pellets-execution.lock")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if lock, err := AcquireRecovery(dir); domain.PublicError(err).Code != "workspace_execution_recovery_required" {
				if lock != nil {
					lock.Close()
				}
				t.Fatalf("ambiguous receipt accepted: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != data {
				t.Fatalf("receipt changed: %q %v", after, err)
			}
		})
	}
}
