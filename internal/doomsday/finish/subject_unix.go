//go:build !windows

package finish

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func hardLinked(_ string, info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}

// openRegular opens path for reading without following a symbolic link in its last element (O_NOFOLLOW) and without waiting on a
// pipe (O_NONBLOCK), and returns it only if the open handle is a regular file and the very file the walk saw (info). Directories
// above the file are not checked: a parent swapped for a link between the walk and the open is not caught here.
func openRegular(path string, info fs.FileInfo) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() || !os.SameFile(st, info) {
		f.Close()
		return nil, fmt.Errorf("changed after it was looked at, or is not a regular file")
	}
	return f, nil
}
