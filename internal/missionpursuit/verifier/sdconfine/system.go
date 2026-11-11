package sdconfine

import (
	"context"
	"time"
)

// runResult is what a started program said; Code is its exit status, or -1 where it could not be started or waited for (Err).
type runResult struct {
	Stdout, Stderr string
	Code           int
	Err            error
}

// system is everything the confiner asks of the machine, so the refusal ladder, the argv and the release are tested with a fake
// and only the real tests start a unit.
type system struct {
	goos string
	uid  int
	pid  int
	// procStart is the start time of process pid (field 22 of /proc/<pid>/stat), or an error when there is no such process.
	procStart func(pid int) (string, error)
	// getenv reads the host's environment (only XDG_RUNTIME_DIR is ever read).
	getenv func(string) string
	// trusted finds a root-owned program in /usr/bin or /bin that others cannot write; never a name on PATH.
	trusted func(name string) (string, error)
	// checkRuntimeDir says the runtime dir is this user's private folder; checkBus that the session bus socket is there.
	checkRuntimeDir func(dir string) error
	checkBus        func(path string) error
	readFile        func(path string) ([]byte, error)
	// run starts bin with exactly env and args, waits, and returns what it said. Its output is bounded.
	run   func(ctx context.Context, bin string, env []string, args ...string) runResult
	newID func() string
	now   func() time.Time
	sleep func(time.Duration)
}
