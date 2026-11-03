//go:build linux

package stop

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrSecondHost: another host already holds the run directory (Q8: one host only; a second host on the same
// database is refused).
var ErrSecondHost = errors.New("stop: another host already holds this run directory")

func flock(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	if err := unix.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrSecondHost
		}
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}

// LockWitness is the exclusive witness lock: one critical section for every writer.
func (w Witness) LockWitness() (unlock func(), err error) { return flock(w.lockPath(), true) }

// AcquireHost takes host.lock without waiting; the holder is the only host of the run directory.
func AcquireHost(r RunDir) (release func(), err error) {
	if err := r.fsys().MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	return flock(filepath.Join(r.Dir, "host.lock"), false)
}
