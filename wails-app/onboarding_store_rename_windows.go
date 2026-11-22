//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// errSharingViolation is ERROR_SHARING_VIOLATION (32).
const errSharingViolation syscall.Errno = 32

// guiStateRenameAttempts bounds the wait for another handle on the target
// (an antivirus scan, an indexer, a second reader) to close: about two seconds
// in all, as internal/account's replaceFile waits.
const guiStateRenameAttempts = 12

// replaceGuiStateFile renames from over to, retrying while the target is
// busy: MoveFileEx (os.Rename) cannot replace a file another handle has open,
// even one opened with delete sharing. The pattern of internal/account's
// replaceFile, which that package does not export.
func replaceGuiStateFile(from, to string) error {
	var err error
	for attempt := 0; attempt < guiStateRenameAttempts; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, errSharingViolation) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return err
}
