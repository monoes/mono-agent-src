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

// privateToUs: fi (the Lstat of path) belongs to this user and has none of the mode bits in loose set.
func privateToUs(_ string, fi os.FileInfo, loose os.FileMode) bool {
	return ownedByUs(fi) && fi.Mode().Perm()&loose == 0
}

// makeRegistryDir creates dir (and its parents) for this user alone.
func makeRegistryDir(dir string) error { return os.MkdirAll(dir, 0o700) }
