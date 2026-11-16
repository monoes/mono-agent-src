//go:build windows

package storage

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive LockFileEx lock on byte 0 of f and waits for
// whichever process is migrating the same database (no
// LOCKFILE_FAIL_IMMEDIATELY), like the blocking flock on unix.
//
// The kernel releases the lock when the handle closes or the process dies, so
// there is no heartbeat, staleness or PID logic. Locks belong to the file
// handle: two opens of the lock file in one process also contend, as with
// flock. os.OpenFile handles are non-inheritable, so child processes (monomind
// and the like) never hold the lock, and they are synchronous (no
// FILE_FLAG_OVERLAPPED), so a zeroed Overlapped with offset 0 is enough. The
// lock file is separate from the database file, leaving SQLite's own Windows
// locking on the database unaffected. It is held only for the migration
// (callers defer unlockFile).
func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol)
	if err != nil {
		return fmt.Errorf("LockFileEx %s: %w", f.Name(), err)
	}
	return nil
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol); err != nil {
		return fmt.Errorf("UnlockFileEx %s: %w", f.Name(), err)
	}
	return nil
}

// tryLockFile takes the exclusive lock if nobody holds it (LOCKFILE_FAIL_IMMEDIATELY);
// false means it is held.
func tryLockFile(f *os.File) (bool, error) {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err == nil {
		return true, nil
	}
	if err == windows.ERROR_LOCK_VIOLATION {
		return false, nil
	}
	return false, fmt.Errorf("LockFileEx %s: %w", f.Name(), err)
}
