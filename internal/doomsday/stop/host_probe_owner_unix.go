//go:build !windows

package stop

import (
	"os"
	"strconv"
	"syscall"
)

// ownerOf names the owner of a file for an error message.
func ownerOf(fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return "uid " + strconv.Itoa(int(st.Uid))
	}
	return "an unknown user"
}

// fileOwnedByMe: the file's owner is the current user (a test seam). A file whose owner cannot be read is not mine.
var fileOwnedByMe = func(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
