package testhome

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// removeAttempts and removeWait bound the retry that removes the test home: on
// Windows a directory stays busy for a moment after its last user is done
// (SQLite connections, the process's own temp files, virus scanners).
const (
	removeAttempts = 30
	removeWait     = 100 * time.Millisecond
)

// removeHome removes dir, retrying a bounded number of times, and writes a
// one-line note to w when it still fails. It never changes the run's exit
// code. os.RemoveAll deletes a symlink or junction itself and does not follow
// it, so nothing outside dir is touched.
func removeHome(dir string, remove func(string) error, sleep func(time.Duration), w io.Writer) {
	var err error
	for i := 0; i < removeAttempts; i++ {
		if err = remove(dir); err == nil {
			return
		}
		if i < removeAttempts-1 {
			sleep(removeWait)
		}
	}
	fmt.Fprintf(w, "testhome: could not remove %s: %v\n", dir, err)
}

// leaveHome moves the process out of dir when its working directory is inside
// it: Windows cannot remove a directory that is a process's working directory.
func leaveHome(dir string) {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	if inside(cwd, dir) {
		_ = os.Chdir(filepath.Dir(dir))
	}
}

// inside reports whether path is dir or lies under it, comparing the
// symlink-resolved forms when both resolve.
func inside(path, dir string) bool {
	if within(path, dir) {
		return true
	}
	p, err1 := filepath.EvalSymlinks(path)
	d, err2 := filepath.EvalSymlinks(dir)
	return err1 == nil && err2 == nil && within(p, d)
}
