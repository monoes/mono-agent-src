//go:build linux || darwin

package stop

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// afterFlock is a test seam: it runs after the lock is taken and before the path is compared with the locked file.
var afterFlock func(path string)

// held are the locks this process holds, by path, so a later look can compare the locked file with the file at the path.
// A same-user process that unlinks a held lock file lets another process lock a new one; the holder finds out here.
// (Windows needs none: its lock handle is opened without FILE_SHARE_DELETE, so the file cannot be deleted or renamed.)
var (
	heldMu sync.Mutex
	held   = map[string]*os.File{}
)

// verifyHeld: nil when this process does not hold path, or holds it and path still leads to the locked file.
func verifyHeld(path string) error {
	heldMu.Lock()
	f := held[filepath.Clean(path)]
	heldMu.Unlock()
	if f != nil && !sameAsPath(f, path) {
		return ErrHostLockLost
	}
	return nil
}

var errLockReplaced = errors.New("stop: the lock file was replaced while it was being locked")

// flock locks path exclusively. After the lock is taken the path must still lead to the locked file: a lock file that
// was unlinked and re-created by another process in between would be a lock nobody else sees (a second host could lock
// the new file). It retries on the new file a few times, then refuses.
func flock(path string, wait bool) (func(), error) {
	for i := 0; i < 3; i++ {
		unlock, err := flockOnce(path, wait)
		if !errors.Is(err, errLockReplaced) {
			return unlock, err
		}
	}
	return nil, errLockReplaced
}

func flockOnce(path string, wait bool) (func(), error) {
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
	if afterFlock != nil {
		afterFlock(path)
	}
	if !sameAsPath(f, path) {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
		return nil, errLockReplaced
	}
	key := filepath.Clean(path)
	heldMu.Lock()
	held[key] = f
	heldMu.Unlock()
	return func() {
		heldMu.Lock()
		if held[key] == f {
			delete(held, key)
		}
		heldMu.Unlock()
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}

func sameAsPath(f *os.File, path string) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(path)
	return err == nil && os.SameFile(a, b)
}
