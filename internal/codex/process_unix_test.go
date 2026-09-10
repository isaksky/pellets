//go:build darwin || linux

package codex

import (
	"os/exec"
	"syscall"
	"testing"
)

// This is the same session escape used by normal Codex shell/PTY execution.
func detachTestDescendant(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func TestDescendantDiscoveryRejectsReusedAndUnrelatedIdentities(t *testing.T) {
	p := &processTree{pid: 10, rootStarted: 100, known: map[int]processIdentity{
		10: {pid: 10, started: 100}, 20: {pid: 20, started: 110},
	}}
	table := map[int]processIdentity{
		10: {pid: 10, parent: 1, group: 10, started: 200}, // Root PID reused.
		11: {pid: 11, parent: 10, group: 10, started: 201},
		20: {pid: 20, parent: 1, group: 20, started: 210}, // Descendant PID reused.
		21: {pid: 21, parent: 1, group: 21, started: 150}, // Unrelated daemon.
	}
	if err := p.discover(table); err != nil {
		t.Fatal(err)
	}
	if len(p.known) != 0 {
		t.Fatalf("adopted unrelated processes: %+v", p.known)
	}
}

func TestDescendantDiscoveryRetainsOrphanedSessions(t *testing.T) {
	p := &processTree{pid: 10, rootStarted: 100, known: map[int]processIdentity{10: {pid: 10, started: 100}}}
	table := map[int]processIdentity{
		10: {pid: 10, parent: 1, group: 10, started: 100},
		20: {pid: 20, parent: 10, group: 20, started: 110},
		30: {pid: 30, parent: 20, group: 30, started: 120},
	}
	if err := p.discover(table); err != nil {
		t.Fatal(err)
	}
	delete(table, 10)
	child := table[20]
	child.parent = 1
	table[20] = child
	if err := p.discover(table); err != nil {
		t.Fatal(err)
	}
	if len(p.known) != 2 || p.known[20].started != 110 || p.known[30].started != 120 {
		t.Fatalf("lost known detached descendants: %+v", p.known)
	}
}
