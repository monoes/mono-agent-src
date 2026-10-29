package chat

import "errors"

// A pinned process is a handle on one process that keeps naming it if its pid is reused, so a
// check made on it and the signal sent to it are about the same process.
type pinnedProcess interface {
	// alive says the pinned process still exists (a zombie counts).
	alive() bool
	signal(sig signalNumber) error
	// pinsPid is true when the handle is the kernel's (pidfd); false when it only holds the number,
	// so the caller has to check again right before it signals.
	pinsPid() bool
	close()
}

var (
	// errNoProcess is the only error that means a pid is gone.
	errNoProcess = errors.New("no such process")
	// errCannotPin means the process cannot be told apart safely here: it is not stopped.
	errCannotPin = errors.New("cannot be pinned")
)

// pinProcess is a seam for tests.
var pinProcess = pinProcessOS
