//go:build darwin || linux

package codex

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Ancestry, not process-group membership, owns Unix descendants: Codex's normal
// PTY/shell children create new sessions. Start times distinguish reused PIDs.
type processIdentity struct {
	pid, parent, group int
	started            uint64
	stopped, zombie    bool
}

type processTree struct {
	custodian                  *processCustodian
	pid                        int
	rootStarted                uint64
	once                       sync.Once
	err                        error
	mu                         sync.Mutex
	known                      map[int]processIdentity
	stopTracking, trackingDone chan struct{}
	stopping                   bool
}

func startProcessTree(cmd *exec.Cmd) (*processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	root, err := lookupProcess(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("inspect owned Codex process identity: %w", err)
	}
	p := &processTree{pid: cmd.Process.Pid, rootStarted: root.started, known: map[int]processIdentity{root.pid: root}, stopTracking: make(chan struct{}), trackingDone: make(chan struct{})}
	go func() {
		defer close(p.trackingDone)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopTracking:
				return
			case <-ticker.C:
				_ = p.refresh()
			}
		}
	}()
	return p, nil
}

// Called before protocol messages are delivered as well as periodically. This
// retains descendants that later become orphans when the app-server exits.
func (p *processTree) refresh() error {
	if p.custodian != nil {
		return p.custodian.refresh()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return nil
	}
	table, err := processSnapshot()
	if err == nil {
		err = p.discover(table)
	}
	return err
}

func (p *processTree) discover(table map[int]processIdentity) error {
	for pid, known := range p.known {
		if current, ok := table[pid]; !ok || current.started != known.started {
			delete(p.known, pid)
		}
	}
	// Inherited members of the invocation's private session/process group
	// remain identifiable even if a short-lived probe exits before a sample.
	// Never adopt the group if its leader PID now denotes a different process.
	leader, leaderExists := table[p.pid]
	if !leaderExists || leader.started == p.rootStarted {
		for pid, candidate := range table {
			if candidate.group == p.pid && candidate.started >= p.rootStarted {
				if _, known := p.known[pid]; !known && len(p.known) >= 4096 {
					return fmt.Errorf("Codex descendant tracking exceeds 4096 processes")
				}
				p.known[pid] = candidate
			}
		}
	}
	for {
		added := false
		for pid, candidate := range table {
			if _, ok := p.known[pid]; ok {
				continue
			}
			parent, owned := p.known[candidate.parent]
			if !owned || table[candidate.parent].started != parent.started {
				continue
			}
			if len(p.known) >= 4096 {
				return fmt.Errorf("Codex descendant tracking exceeds 4096 processes")
			}
			p.known[pid] = candidate
			added = true
		}
		if !added {
			return nil
		}
	}
}

func signalIdentity(identity processIdentity, signal syscall.Signal) error {
	current, err := lookupProcess(identity.pid)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.started != identity.started || current.zombie {
		return nil
	}
	err = syscall.Kill(identity.pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// The custodian uses this after an uncertain termination attempt. Observation
// does not unlock, retry execution, or adopt reused PIDs. New descendants of a
// still-live owned ancestor remain discoverable until the entire tree stops.
func (p *processTree) confirmStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	table, err := processSnapshot()
	if err != nil || p.discover(table) != nil {
		return false
	}
	for pid, identity := range p.known {
		current, exists := table[pid]
		if exists && current.started == identity.started && !current.zombie {
			return false
		}
	}
	return true
}

func (p *processTree) terminate() error {
	if p.custodian != nil {
		return p.custodian.terminate()
	}
	p.once.Do(func() {
		close(p.stopTracking)
		<-p.trackingDone
		p.mu.Lock()
		defer p.mu.Unlock()
		p.stopping = true
		// Freeze the live root first, then discover/freeze descendants until no
		// owned process can fork. Re-scan after observing their stopped states.
		if root, ok := p.known[p.pid]; ok {
			p.err = errors.Join(p.err, signalIdentity(root, syscall.SIGSTOP))
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			table, err := processSnapshot()
			if err != nil {
				p.err = errors.Join(p.err, err)
				break
			}
			if err := p.discover(table); err != nil {
				p.err = errors.Join(p.err, err)
				break
			}
			allStopped := true
			for pid, identity := range p.known {
				if table[pid].stopped || table[pid].zombie {
					continue
				}
				allStopped = false
				if err := signalIdentity(identity, syscall.SIGSTOP); err != nil {
					p.err = errors.Join(p.err, err)
				}
			}
			if allStopped {
				break
			}
			if time.Now().After(deadline) {
				p.err = errors.Join(p.err, fmt.Errorf("Codex descendants did not stop before cleanup deadline"))
				break
			}
			time.Sleep(time.Millisecond)
		}
		// Root last. Individual identity-checked signals cross setsid/setpgid
		// boundaries without targeting names, unrelated PIDs, or foreign groups.
		for pid, identity := range p.known {
			if pid != p.pid {
				p.err = errors.Join(p.err, signalIdentity(identity, syscall.SIGKILL))
			}
		}
		if root, ok := p.known[p.pid]; ok {
			p.err = errors.Join(p.err, signalIdentity(root, syscall.SIGKILL))
		}
		deadline = time.Now().Add(2 * time.Second)
		for {
			active := false
			for _, identity := range p.known {
				current, err := lookupProcess(identity.pid)
				if errors.Is(err, syscall.ESRCH) {
					continue
				}
				if err != nil {
					p.err = errors.Join(p.err, err)
					continue
				}
				if current.started == identity.started && !current.zombie {
					active = true
				}
			}
			if !active {
				break
			}
			if time.Now().After(deadline) {
				p.err = errors.Join(p.err, fmt.Errorf("Codex descendants remained active after SIGKILL"))
				break
			}
			time.Sleep(time.Millisecond)
		}
	})
	if p.err != nil {
		return errors.Join(ErrCleanup, p.err)
	}
	return p.err
}
