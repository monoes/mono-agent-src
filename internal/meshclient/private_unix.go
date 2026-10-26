//go:build !windows

package meshclient

import (
	"os"
	"syscall"
)

// privateDirProblem says why dir is not a directory only this user can use, or "".
func privateDirProblem(dir string) string {
	fi, err := os.Lstat(dir)
	if err != nil {
		return "mesh directory does not exist"
	}
	if !fi.IsDir() {
		return "mesh path is not a private directory"
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "mesh directory is not private (wrong owner)"
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "mesh directory is not private (group/other access)"
	}
	return ""
}
