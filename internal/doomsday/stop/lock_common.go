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
func (w Witness) LockWitness() (unlock func(), err error) {
	if err := VerifyHostLock(w.Dir); err != nil {
		return nil, err
	}
	return flock(w.lockPath(), true)
}

// AcquireHost takes host.lock without waiting; the holder is the only host of the run directory.
func AcquireHost(r RunDir) (release func(), err error) {
	if err := r.fsys().MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	return flock(filepath.Join(r.Dir, "host.lock"), false)
}

// ErrHostLockLost: the host lock this process holds is no longer the file at its name (it was unlinked or replaced), so a
// second host could lock a new file; nothing may go on as the only host.
var ErrHostLockLost = errors.New("stop: the host lock file was removed or replaced, so this host is no longer the only one")

// VerifyHostLock is the look: nil when this process holds no host lock for r (an injected one, a CLI command) or holds
// it and the file at its name is still the locked one. Windows needs no look: the lock handle forbids delete and rename.
func VerifyHostLock(r RunDir) error { return verifyHeld(filepath.Join(r.Dir, "host.lock")) }
