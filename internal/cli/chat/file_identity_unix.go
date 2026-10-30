//go:build !windows

package chat

import (
	"os"
	"syscall"
)

// fileIdentity is the device and inode of a file, which tell it from any other file with the same name.
func fileIdentity(fi os.FileInfo) (dev, ino uint64, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true //nolint:unconvert // the field types differ between platforms
}
