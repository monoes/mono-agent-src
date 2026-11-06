//go:build !windows

package stop

import (
	"os"
	"syscall"
)

// fileOwnedByMe: the file's owner is the current user (a test seam). A file whose owner cannot be read is not mine.
var fileOwnedByMe = func(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
