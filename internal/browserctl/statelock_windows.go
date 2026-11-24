//go:build windows

package browserctl

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive LockFileEx lock on byte 0 and waits for whichever
// process holds it.
func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		return fmt.Errorf("LockFileEx %s: %w", f.Name(), err)
	}
	return nil
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
