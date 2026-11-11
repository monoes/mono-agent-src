package finish

import (
	"fmt"
	"os"
	"syscall"
)

// statExtra: see statextra_linux.go.
func statExtra(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("|%d|%d", st.Ino, st.Ctimespec.Nano())
	}
	return ""
}
