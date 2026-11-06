//go:build !windows

package finish

import (
	"io/fs"
	"syscall"
)

func hardLinked(_ string, info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
