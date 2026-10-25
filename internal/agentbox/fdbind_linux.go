//go:build linux

package agentbox

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// binder pins the sources of the binds through O_PATH descriptors. A path checked and then
// handed to bubblewrap by name could be swapped for a symlink in between (a boxed turn
// sharing the project can do that); a descriptor names the object that was checked.
type binder struct{ files []*os.File }

const (
	wantAny = iota
	wantFile
	wantDir
)

// pin opens src without following a symlink at the last component (after resolving the
// path once), checks its type, and confirms that the descriptor still names the resolved
// path. It returns that canonical path and the descriptor's index.
func (bd *binder) pin(src string, want int) (string, int, error) {
	real0, err := filepath.EvalSymlinks(src)
	if err != nil {
		return "", 0, errf(CodeBadPath, "%s: %v", src, err)
	}
	fd, err := unix.Open(real0, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", 0, errf(CodeBadPath, "%s: %v", src, err)
	}
	f := os.NewFile(uintptr(fd), real0)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		f.Close()
		return "", 0, errf(CodeBadPath, "%s: %v", src, err)
	}
	typ := st.Mode & unix.S_IFMT
	switch {
	case typ == unix.S_IFLNK:
		f.Close()
		return "", 0, errf(CodeBadPath, "%s is a symlink: it changed while it was being checked", src)
	case want == wantFile && typ != unix.S_IFREG:
		f.Close()
		return "", 0, errf(CodeBadPath, "%s is not a regular file (a symlink or directory cannot be bound into the box)", src)
	case want == wantDir && typ != unix.S_IFDIR:
		f.Close()
		return "", 0, errf(CodeBadPath, "%s is not a directory", src)
	}
	real, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil || real != real0 {
		f.Close()
		return "", 0, errf(CodeBadPath, "%s changed while it was being checked", src)
	}
	bd.files = append(bd.files, f)
	return real, len(bd.files) - 1, nil
}

// arg is the bwrap bind of descriptor idx at dest; the first extra file is descriptor 3.
func fdBind(flag string, idx int, dest string) []string {
	return []string{flag + "-fd", fmt.Sprint(3 + idx), dest}
}

func (bd *binder) closeAll() {
	for _, f := range bd.files {
		f.Close()
	}
}
