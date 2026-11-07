//go:build darwin

package chat

import (
	"errors"

	"golang.org/x/sys/unix"
)

// awaitExitNoReap returns once the child pid has exited, leaving it unreaped (kqueue's NOTE_EXIT reports the exit
// without waiting for the child): the pid, and with it the process group named by it, stays this child's until cmd.Wait
// collects it. It reports whether it held the child that way; a number that is neither running nor an unreaped
// zombie is not held.
func awaitExitNoReap(pid int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	var out [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(kq, change, out[:], nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return isZombie(pid)
		}
		switch {
		case n == 1 && out[0].Flags&unix.EV_ERROR != 0:
			return unix.Errno(out[0].Data) == unix.ESRCH && isZombie(pid) // already exited: held only if still unreaped
		case n == 1 && out[0].Fflags&unix.NOTE_EXIT != 0:
			return isZombie(pid) || unix.Kill(pid, 0) == nil
		}
		change = nil // registered; wait again
	}
}

// isZombie says whether pid is a process that has exited and not been waited for.
func isZombie(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err == nil && k != nil && k.Proc.P_stat == 5 /* SZOMB */
}
