//go:build !windows

package update

import (
	"os"
	"os/exec"
	"syscall"
)

// uidOf is the user id the owner checks compare with; a variable so a test can be another user.
var uidOf = os.Getuid

func ownedByUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uidOf()
}

// ownedByUserOrRoot: what a program this user may run belongs to.
func ownedByUserOrRoot(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && (int(st.Uid) == uidOf() || st.Uid == 0)
}

// isRoot is a variable so a test can stand in for the root user.
var isRoot = func() bool { return os.Geteuid() == 0 }

// ownProcessGroup puts cmd in a process group of its own, so that the group (the program and whatever it started) can be
// killed: on the timeout through cmd.Cancel, and by the returned function once the answer is in.
func ownProcessGroup(cmd *exec.Cmd) (killGroup func()) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	kill := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	cmd.Cancel = func() error { kill(); return nil }
	return kill
}
