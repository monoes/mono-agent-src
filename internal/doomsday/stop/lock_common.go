//go:build linux || darwin || windows

package stop

import (
	"errors"
	"path/filepath"
)

// ErrSecondHost: another host already holds the run directory (Q8: one host only; a second host on the same
// database is refused).
var ErrSecondHost = errors.New("stop: another host already holds this run directory")

// flock is the platform lock on a file: lock_unix.go (flock) and lock_windows.go (LockFileEx).

// LockWitness is the exclusive witness lock: one critical section for every writer.
func (w Witness) LockWitness() (unlock func(), err error) { return flock(w.lockPath(), true) }

// AcquireHost takes host.lock without waiting; the holder is the only host of the run directory.
func AcquireHost(r RunDir) (release func(), err error) {
	if err := r.fsys().MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	return flock(filepath.Join(r.Dir, "host.lock"), false)
}
