//go:build darwin

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
	// StopAbsent: the leader is confirmed gone (no such process, or the number now belongs to a process with another
	// start stamp). Nothing was signalled.
	StopAbsent StopOutcome = iota
	// StopStopped: the leader was present, was signalled, and then no longer exists.
	StopStopped
	// StopHeld: the check or the stop could not be completed (no permission, an unreadable stamp, a leader that did not
	// exit). That is NOT evidence that the leader is gone: the session must be held, not settled.
	StopHeld
)

// StopResult is the outcome and, for StopHeld, the cause (a fixed text for the operator).
type StopResult struct {
	Outcome StopOutcome
	Cause   string
}

// Stopper stops the leader of a turn. macOS has no pidfd, so a signal goes by number: the process is pinned by its pid
// and start time (a Handle), that pair is checked again immediately before each signal, and nothing is signalled whose
// stamp is not the recorded one. The window between that check and the signal is the residual a pidfd does not have.
// What else of the turn still runs is the stray reconciliation's business, as on Linux.
type Stopper struct {
	// StampOf reads a process's start stamp ("" when it cannot be read); the same function that recorded the stamp.
	StampOf func(pid int) string
	// Same compares the recorded stamp with the current one (nil: equality).
	Same func(recorded, current string) bool
	// Grace is how long the leader gets after SIGTERM before SIGKILL, and then after SIGKILL to exit.
	Grace time.Duration

	// seam for tests: the signal, to a pid or (negative) a process group
	kill func(pid int, sig unix.Signal) error
}

func (s Stopper) send(pid int, sig unix.Signal) error {
	if s.kill != nil {
		return s.kill(pid, sig)
	}
	return unix.Kill(pid, sig)
}

func held(format string, a ...any) StopResult {
	return StopResult{Outcome: StopHeld, Cause: fmt.Sprintf(format, a...)}
}

// waitGone waits up to d for the process the handle names to be gone.
func waitGone(h Handle, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !h.Alive() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// StopLeader checks that the process pid with start stamp is still the recorded leader and, if it is, stops it.
func (s Stopper) StopLeader(pid int, stamp string) StopResult {
	if pid <= 1 || stamp == "" || s.StampOf == nil {
		return held("the leader of the turn cannot be identified (no pid or no start stamp was recorded)")
	}
	h, err := NewProcHandle(pid)
	if err != nil { // no such process, or only a zombie of it
		return StopResult{Outcome: StopAbsent}
	}
	got := s.StampOf(pid)
	if got == "" {
		if !h.Alive() {
			return StopResult{Outcome: StopAbsent}
		}
		return held("the start stamp of the turn's process could not be read")
	}
	if !h.Alive() {
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
	// A leader that leads its own process group (a bound turn is started with a session of its own): the whole group is
	// killed at once so the children it left behind die with it; this is recovery of a turn whose host died.
	if pg, perr := unix.Getpgid(pid); perr == nil && pg == pid && h.Alive() {
		switch gerr := s.send(-pid, unix.SIGKILL); {
		case gerr == nil:
			if waitGone(h, grace) {
				return StopResult{Outcome: StopStopped}
			}
			return held("the turn's process group did not exit after SIGKILL")
		case errors.Is(gerr, unix.ESRCH):
			return StopResult{Outcome: StopStopped}
		default:
			return held("the turn's process group could not be signalled (%v)", errnoText(gerr))
		}
	}
	for _, step := range []struct {
		sig  unix.Signal
		wait time.Duration
	}{{unix.SIGTERM, grace}, {unix.SIGKILL, grace}} {
		if !h.Alive() { // it exited, or the number is another process's now: nothing more is signalled
			return StopResult{Outcome: StopStopped}
		}
		if err := s.send(pid, step.sig); err != nil {
			if errors.Is(err, unix.ESRCH) {
				return StopResult{Outcome: StopStopped}
			}
			return held("the turn's process could not be signalled (%v)", errnoText(err))
		}
		if waitGone(h, step.wait) {
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
