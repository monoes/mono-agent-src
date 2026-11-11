package finish

import (
	"fmt"
	"os"
	"syscall"
)

// statExtra is the inode and the change time of an entry, added to the stat manifest: a rewrite that keeps a file's size and
// modification time still moves its change time (a restored mtime does not hide it).
func statExtra(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("|%d|%d", st.Ino, st.Ctim.Nano())
	}
	return ""
}
