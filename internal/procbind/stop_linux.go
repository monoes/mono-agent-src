//go:build linux

package procbind

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StopOutcome is what StopLeader found.
type StopOutcome int

const (
	// StopAbsent: the leader is confirmed gone (no such process, its pidfd polls dead, or the number now belongs to a
	// process with another start stamp). Nothing was signalled.
	StopAbsent StopOutcome = iota
	// StopStopped: the leader was present, was signalled through its own pidfd, and its pidfd then polled readable: it
	// has exited.
	StopStopped
	// StopHeld: the check or the stop could not be completed (no descriptor, no permission, an unreadable stamp, a
	// leader that did not exit). That is NOT evidence that the leader is gone: the session must be held, not settled.
	StopHeld
)

// StopResult is the outcome and, for StopHeld, the cause (a fixed text for the operator).
type StopResult struct {
	Outcome StopOutcome
	Cause   string
}

// Stopper stops the leader of a turn by its own pidfd. It signals nothing else: no process group, no session, no
// child: a group id can be reused between the last check and the signal, and a leader that is already gone leaves
// nothing to pin. What else of the turn still runs (tool children, a runtime process) is the existing marker-based
// stray reconciliation's business, which signals a process only when its identity is pinned and otherwise reports it.
type Stopper struct {
	// StampOf reads a process's start stamp ("" when it cannot be read); the same function that recorded the stamp.
	StampOf func(pid int) string
	// Same compares the recorded stamp with the current one (nil: equality).
	Same func(recorded, current string) bool
	// Grace is how long the leader gets after SIGTERM before SIGKILL, and then after SIGKILL to exit.
	Grace time.Duration

	// seams for tests
	openPidfd func(pid int) (int, error)
	signal    func(fd int, sig unix.Signal) error
}

func (s Stopper) open(pid int) (int, error) {
	if s.openPidfd != nil {
		return s.openPidfd(pid)
	}
	return unix.PidfdOpen(pid, 0)
}

func (s Stopper) send(fd int, sig unix.Signal) error {
	if s.signal != nil {
		return s.signal(fd, sig)
	}
	return unix.PidfdSendSignal(fd, sig, nil, 0)
}

// alive polls the pidfd: it becomes readable when the process exits, reaped or not. A poll that fails is not "alive":
// the caller treats it as unable to check.
func pollAlive(fd int) (alive bool, err error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		return n == 0, nil
	}
}

// waitExit waits up to d for the pidfd to become readable.
func waitExit(fd int, d time.Duration) (exited bool, err error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left < 0 {
			left = 0
		}
		n, err := unix.Poll(fds, int(left/time.Millisecond)+1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
}

func held(format string, a ...any) StopResult {
	return StopResult{Outcome: StopHeld, Cause: fmt.Sprintf(format, a...)}
}

// StopLeader checks that the process pid with start stamp is still the recorded leader and, if it is, stops it.
func (s Stopper) StopLeader(pid int, stamp string) StopResult {
	if pid <= 1 || stamp == "" || s.StampOf == nil {
		return held("the leader of the turn cannot be identified (no pid or no start stamp was recorded)")
	}
	fd, err := s.open(pid)
	switch {
	case errors.Is(err, unix.ESRCH):
		return StopResult{Outcome: StopAbsent}
	case err != nil:
		return held("the turn's process could not be opened (%v)", errnoText(err))
	}
	defer unix.Close(fd)
	alive, err := pollAlive(fd)
	if err != nil {
		return held("the turn's process could not be polled (%v)", errnoText(err))
	}
	if !alive {
		return StopResult{Outcome: StopAbsent}
	}
	got := s.StampOf(pid)
	if got == "" {
		return held("the start stamp of the turn's process could not be read")
	}
	alive, err = pollAlive(fd)
	if err != nil {
		return held("the turn's process could not be polled (%v)", errnoText(err))
	}
	if !alive {
		return StopResult{Outcome: StopAbsent}
	}
	same := got == stamp
	if s.Same != nil {
		same = s.Same(stamp, got)
	}
	if !same { // the number belongs to another process now (or the same number of another boot), so ours is gone
		return StopResult{Outcome: StopAbsent}
	}
	grace := s.Grace
	if grace <= 0 {
		grace = 15 * time.Second
	}
	for _, step := range []struct {
		sig  unix.Signal
		wait time.Duration
	}{{unix.SIGTERM, grace}, {unix.SIGKILL, grace}} {
		if err := s.send(fd, step.sig); err != nil {
			if errors.Is(err, unix.ESRCH) { // it exited since the check
				return StopResult{Outcome: StopStopped}
			}
			return held("the turn's process could not be signalled (%v)", errnoText(err))
		}
		exited, err := waitExit(fd, step.wait)
		if err != nil {
			return held("the turn's process could not be waited for (%v)", errnoText(err))
		}
		if exited {
			return StopResult{Outcome: StopStopped}
		}
	}
	return held("the turn's process did not exit after SIGKILL")
}

// errnoText names an errno without any path or text the system added.
func errnoText(err error) string {
	var en unix.Errno
	if errors.As(err, &en) {
		return en.Error()
	}
	return "an error"
}
