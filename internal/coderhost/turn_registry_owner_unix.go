//go:build !windows

package coderhost

import (
	"os"
	"syscall"
)

// ownedByUs: the file belongs to the user running this process.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
