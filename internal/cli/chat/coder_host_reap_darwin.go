//go:build darwin

package chat

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/procchain"
)

// reapPoll is how long one wait in the kernel lasts before the child is looked at again.
var reapPoll = time.Second

// awaitExitNoReap returns once the child pid has exited, leaving it unreaped (kqueue's NOTE_EXIT reports the exit
// without waiting for the child): the pid, and with it the process group named by it, stays this child's until cmd.Wait
// collects it. It reports whether it held the child that way; a number that is neither running nor an unreaped
// zombie is not held. The wait is in slices of reapPoll: the thread is back in Go between slices, never blocked in the kernel
// for longer than one.
func awaitExitNoReap(pid int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
	var out [1]unix.Kevent_t
	slice := unix.NsecToTimespec(int64(reapPoll))
	for {
		n, err := unix.Kevent(kq, change, out[:], &slice)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return isZombie(pid)
		}
		switch {
		case n == 0: // nothing yet: wait again, so no single wait in the kernel lasts longer than a slice
			change = nil
		case out[0].Flags&unix.EV_ERROR != 0:
			return unix.Errno(out[0].Data) == unix.ESRCH && isZombie(pid) // already exited: held only if still unreaped
		case out[0].Fflags&unix.NOTE_EXIT != 0:
			return isZombie(pid)
		default:
			change = nil
		}
	}
}

// isZombie says whether pid is a process that has exited and not been waited for.
func isZombie(pid int) bool {
	e, ok := procchain.ReadEntry(pid)
	return ok && e.Zombie
}
