package stop

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// sessEIOTries and sessEIOPause bound how long a table entry that answers EIO is waited for: the kernel answers EIO for the entry
// of a process that is past its last instruction in user space but not yet a zombie (measured on macOS 27.2 while members were
// being killed; it lasts a few milliseconds).
const (
	sessEIOTries = 20
	sessEIOPause = time.Millisecond
)

// settleEIO runs read, retrying only while it answers EIO. An entry that STILL answers EIO after the bound is read as gone only
// when the kernel itself says the process does not exist (probe is kill(pid, 0): ESRCH); a process that can still be signalled
// (success, or EPERM for another user's) is not gone, whatever its table entry says, so the answer is an error and the proof
// above it is NOT PROVEN, never "nothing there". Any other error is returned at once.
func settleEIO[T any](pid int, read func() (T, error), probe func(pid int) error, pause func()) (T, error) {
	var zero T
	for try := 0; ; try++ {
		v, err := read()
		if err == nil || !errors.Is(err, syscall.EIO) {
			return v, err
		}
		if try >= sessEIOTries {
			if errors.Is(probe(pid), syscall.ESRCH) {
				return zero, errSessGone
			}
			return zero, fmt.Errorf("the table entry of process %d cannot be read and the process can still be signalled, so it is not taken for gone: %w", pid, err)
		}
		pause()
	}
}
