//go:build windows

package procbind

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// StopOutcome is what StopLeader found.
type StopOutcome int

const (
	// StopAbsent: the leader is confirmed gone (no process has the number, the process that has it has exited, or the
	// number now belongs to a process with another start stamp). Nothing was ended.
	StopAbsent StopOutcome = iota
	// StopStopped: the leader was present, was ended through a handle opened on it, and that handle then showed it exited.
	StopStopped
	// StopHeld: the check or the stop could not be completed (no access, an unreadable stamp, a leader that did not
	// exit). That is NOT evidence that the leader is gone: the session must be held, not settled.
	StopHeld
)

// StopResult is the outcome and, for StopHeld, the cause (a fixed text for the operator).
type StopResult struct {
	Outcome StopOutcome
	Cause   string
}

// Stopper stops the leader of a turn through a process handle. The handle is opened first and stays open for the whole
// check and stop: while it is open the kernel keeps the process object, so the number cannot be given to another
// process between the stamp check and the end, and the end goes through that handle, never by number. It ends the
// leader only: Windows has no group kill (the worker's CREATE_NEW_PROCESS_GROUP is a console group) and the worker is in
// no job, so what else of the turn still runs is the stray reconciliation's business, as on Linux and macOS.
type Stopper struct {
	// StampOf reads a process's start stamp ("" when it cannot be read); the same function that recorded the stamp.
	StampOf func(pid int) string
	// Same compares the recorded stamp with the current one (nil: equality).
	Same func(recorded, current string) bool
	// Grace is how long the leader gets to be gone after it was ended. Windows has no SIGTERM: TerminateProcess ends
	// the process at once, so there is no graceful step, only the wait for the exit to complete.
	Grace time.Duration
}

func held(format string, a ...any) StopResult {
	return StopResult{Outcome: StopHeld, Cause: fmt.Sprintf(format, a...)}
}

func (s Stopper) same(recorded, current string) bool {
	if s.Same != nil {
		return s.Same(recorded, current)
	}
	return recorded == current
}

// waitGone waits up to d for the process the handle names to exit.
func waitGone(h *ProcHandle, d time.Duration) bool {
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
	h, err := OpenWorkerHandle(pid)
	if err != nil {
		return s.unopened(pid, stamp, err)
	}
	defer h.Close()
	// h pins the number, so StampOf (the function that recorded the stamp, which opens the process by number) reads
	// the process h names, not one that took the number since
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
	if !s.same(stamp, got) { // the number belongs to another process now, so ours is gone
		return StopResult{Outcome: StopAbsent}
	}
	grace := s.Grace
	if grace <= 0 {
		grace = 15 * time.Second
	}
	if err := h.Terminate(); err != nil {
		if !h.Alive() { // it exited since the check (ending a process that is exiting is refused)
			return StopResult{Outcome: StopStopped}
		}
		return held("the turn's process could not be ended (%v)", errnoText(err))
	}
	if waitGone(h, grace) {
		return StopResult{Outcome: StopStopped}
	}
	return held("the turn's process did not exit after it was ended")
}

// unopened decides from a number that could not be opened with the right to end it. The worker's access list gives its
// user that right, so a refusal most likely means the number now belongs to a process of another user or a protected
// one: a handle that can only read it is opened to tell, and nothing is ended through it.
func (s Stopper) unopened(pid int, stamp string, err error) StopResult {
	if noLiveProcess(err) {
		return StopResult{Outcome: StopAbsent}
	}
	q, qerr := OpenProcessHandle(pid)
	if qerr != nil {
		if noLiveProcess(qerr) {
			return StopResult{Outcome: StopAbsent}
		}
		return held("the turn's process could not be opened (%v)", errnoText(err))
	}
	defer q.Close()
	if got := s.StampOf(pid); got != "" && !s.same(stamp, got) {
		return StopResult{Outcome: StopAbsent}
	}
	if !q.Alive() {
		return StopResult{Outcome: StopAbsent}
	}
	return held("the turn's process could not be opened to end it (%v)", errnoText(err))
}

// noLiveProcess: no process has the number (OpenProcess refuses it as a parameter), or the one that has it has exited.
func noLiveProcess(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, errExited)
}

// errnoText names an error code without any path or text the caller added.
func errnoText(err error) string {
	var en windows.Errno
	if errors.As(err, &en) {
		return en.Error()
	}
	return "an error"
}
