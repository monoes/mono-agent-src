//go:build !windows

package chat

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// A host-started turn is its own session leader (clicore.DetachStart), so its
// pid is its process group: signalling -pid reaches the agents under it too.
// A turn placed in a run's process group on macOS (#1611) is not a leader: its group is the run's, shared with the other
// turns. It is signalled by pid together with its own descendants, never by group, so ending one turn leaves the others.
// Both refuse pid 1 or below, which kill(2) would read as "every process".
func terminateGroup(pid int) error { return signalGroup(pid, syscall.SIGTERM) }

func killGroup(pid int) error { return signalGroup(pid, syscall.SIGKILL) }

func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return errors.New("not a turn's pid")
	}
	// A group with no live leader (the leader exited, children remain) has no
	// process whose pgid can be asked; the group id alone is what kill takes.
	if pg, err := syscall.Getpgid(pid); err == nil && pg != pid {
		if pg <= 1 {
			return errors.New("pid is in no process group of a run")
		}
		return signalTree(pid, sig)
	}
	return syscall.Kill(-pid, sig)
}

// signalTree signals pid and every process descended from it, as far as the process table shows them (a process that
// double-forked to init is not seen: the same best effort as the run's group).
func signalTree(pid int, sig syscall.Signal) error {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	kids := map[int][]int{}
	if err == nil {
		for _, ln := range strings.Split(string(out), "\n") {
			f := strings.Fields(ln)
			if len(f) != 2 {
				continue
			}
			c, e1 := strconv.Atoi(f[0])
			p, e2 := strconv.Atoi(f[1])
			if e1 == nil && e2 == nil {
				kids[p] = append(kids[p], c)
			}
		}
	}
	// the leader first, so it cannot start more while its descendants are signalled
	first := syscall.Kill(pid, sig)
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range kids[cur] {
			if c <= 1 || seen[c] {
				continue
			}
			seen[c] = true
			_ = syscall.Kill(c, sig)
			queue = append(queue, c)
		}
	}
	if err != nil && first == nil {
		return errors.New("the turn's descendants could not be listed: " + err.Error())
	}
	return first
}
