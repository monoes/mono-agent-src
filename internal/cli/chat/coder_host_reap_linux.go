//go:build linux

package chat

import "golang.org/x/sys/unix"

// awaitExitNoReap returns once the child pid has exited, leaving it unreaped: the pid, and with it the process group
// named by it, stays this child's until cmd.Wait collects it. It reports whether it held the child that way.
func awaitExitNoReap(pid int) bool {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err == nil
		}
	}
}
