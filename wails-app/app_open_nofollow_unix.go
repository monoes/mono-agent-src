//go:build !windows

package main

import (
	"os"
	"syscall"
)

// openChatFileNoFollow opens p read-only without following a symlink in the
// last component and without blocking on a FIFO or device.
func openChatFileNoFollow(p string) (*os.File, error) {
	fd, err := syscall.Open(p, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: p, Err: err}
	}
	return os.NewFile(uintptr(fd), p), nil
}
