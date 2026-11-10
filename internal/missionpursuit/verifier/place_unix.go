//go:build !windows

package verifier

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"
)

const platformOK = true

// GroupPlace is the mission's containment: every criterion is placed in its own process group (the sandbox sets it; a command
// that is not in one is refused), the sandbox kills the group at its deadline, and when a criterion ends whatever is still
// in its group is killed too. Close ends the placing for good (the run's deadline or end).
//
// It is not a cgroup unit: a child that leaves the group (setsid) is not reached. The result says "process-group".
type GroupPlace struct {
	mu     sync.Mutex
	closed bool
}

// Place satisfies finish.Sandbox.Place.
func (g *GroupPlace) Place(cmd *exec.Cmd) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, errors.New("the run's process group is closed, so nothing more is started")
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		return nil, errors.New("the program is not in its own process group, so it is not started")
	}
	return func() { killLeftovers(cmd) }, nil
}

// Close stops further placing.
func (g *GroupPlace) Close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// killLeftovers kills what remains of the criterion's group after its leader ended. The group id is the leader's pid, which
// the kernel does not hand to another process while a member of the group is alive; once the group is empty the call finds
// nothing. (The window between an empty group and a new process taking that number is the one the finish sandbox avoids by
// never signalling after a reap; here the leftovers of a run are the larger risk.)
func killLeftovers(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
