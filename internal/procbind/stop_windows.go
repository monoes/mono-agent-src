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
	// StopHeld: the check or the stop could not be completed (no access, an unreadable stamp or state, a leader that did
	// not exit). That is NOT evidence that the leader is gone: the session must be held, not settled.
	StopHeld
)

// StopResult is the outcome and, for StopHeld, the cause (a fixed text for the operator).
type StopResult struct {
	Outcome StopOutcome
	Cause   string
}

// Stopper stops the leader of a turn through a process handle. The handle is opened first and stays open for the whole
// check and stop: while it is open the kernel keeps the process object, so the number cannot be given to another
// process between the stamp check and the end, and the end goes through that handle, never by number.
//
// It ends the leader only (Windows has no group kill; the worker's CREATE_NEW_PROCESS_GROUP is a console group). The
// runtime the leader started goes with it all the same: monomind.Exec starts it in a job with KILL_ON_JOB_CLOSE whose
// only handle the leader holds, so ending the leader ends that job's tree. Not when the job could not be made or the
// runtime could not be put in it (the launch then goes on without one), and not for a descendant that left the job
// (it allows breakaway) or was started outside it (through WMI or the Task Scheduler): those outlive the leader, and
// nothing else stops them, because the marker-based stop of a turn's leftover processes cannot pin a process on
// Windows and so stops none (internal/cli/chat/coder_pin_windows.go; SECURITY.md, "Stopping the processes a coder turn
// left running").
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

// waitExit waits up to d for the process the handle names to exit.
func waitExit(h *ProcHandle, d time.Duration) (exited bool, err error) {
	deadline := time.Now().Add(d)
	for {
		if exited, err := h.exitState(); err != nil || exited {
			return exited, err
		}
		if !time.Now().Before(deadline) {
			return false, nil
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
	exited, err := h.exitState()
	switch {
	case err != nil:
		return held("the state of the turn's process could not be read (%v)", errnoText(err))
	case exited:
		return StopResult{Outcome: StopAbsent}
	case got == "":
		return held("the start stamp of the turn's process could not be read")
	case !s.same(stamp, got): // the number belongs to another process now, so ours is gone
		return StopResult{Outcome: StopAbsent}
	}
	grace := s.Grace
	if grace <= 0 {
		grace = 15 * time.Second
	}
	if err := h.Terminate(); err != nil {
		if exited, xerr := h.exitState(); xerr == nil && exited { // it exited since the check (ending an exiting process is refused)
			return StopResult{Outcome: StopStopped}
		}
		return held("the turn's process could not be ended (%v)", errnoText(err))
	}
	exited, err = waitExit(h, grace)
	switch {
	case err != nil:
		return held("the turn's process could not be waited for (%v)", errnoText(err))
	case exited:
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
		return held("the turn's process could not be opened (to end it: %v; to read it: %v)", errnoText(err), errnoText(qerr))
	}
	defer q.Close()
	got := s.StampOf(pid)
	exited, xerr := q.exitState()
	switch {
	case xerr != nil:
		return held("the state of the turn's process could not be read (%v)", errnoText(xerr))
	case exited:
		return StopResult{Outcome: StopAbsent}
	case got != "" && !s.same(stamp, got):
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
