//go:build windows

package stop

import (
	"errors"

	"golang.org/x/sys/windows"
)

// NOT VERIFIED on a real Windows host: this file compiles (GOOS=windows go vet) and the decisions around it are
// tested through winLockOS on Linux; LockFileEx has not run here.

// flock is the exclusive file lock of the host and the witness on Windows.
func flock(path string, wait bool) (func(), error) { return lockVia(realWinLock{}, path, wait) }

type realWinLock struct{}

// LockFile opens the file with a handle that is NOT inheritable (nil security attributes: a child process never gets
// it, so a worker cannot keep the host lock alive or hold the witness lock) and takes LockFileEx on its first byte.
// The lock is per handle: a second handle in this same process contends, as a second process does.
func (realWinLock) LockFile(path string, wait bool) (func(), bool, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, false, err
	}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	var ov windows.Overlapped
	if err := windows.LockFileEx(h, flags, 0, 1, 0, &ov); err != nil {
		_ = windows.CloseHandle(h)
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() {
		var ov windows.Overlapped
		_ = windows.UnlockFileEx(h, 0, 1, 0, &ov)
		_ = windows.CloseHandle(h)
	}, false, nil
}
