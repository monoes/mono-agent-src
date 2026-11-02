//go:build !windows

package orgsign

import (
	"os"
	"syscall"
)

// rawDirCtime reads a directory's ctime straight from the platform's stat, bypassing the guard's own stamp code,
// so a test can tell a filesystem that does not move it from a guard that stopped recording it.
func rawDirCtime(dir string) int64 {
	st, err := os.Lstat(dir)
	if err != nil {
		return 0
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		return ctimeNs(sys)
	}
	return 0
}
