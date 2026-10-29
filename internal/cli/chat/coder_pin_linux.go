//go:build linux

package chat

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

type signalNumber = syscall.Signal

type pidfd struct{ fd int }

// pinProcessOS opens a pidfd: the kernel keeps it naming this process, and a signal sent through it
// can never reach another process that got the pid later. Without pidfd_open (a kernel before 5.3)
// the process is not stopped.
func pinProcessOS(pid int) (pinnedProcess, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	switch {
	case err == nil:
		return &pidfd{fd}, nil
	case errors.Is(err, unix.ESRCH):
		return nil, errNoProcess
	}
	return nil, fmt.Errorf("%w: pidfd_open: %v", errCannotPin, err)
}

// alive: only ESRCH means the process is gone; EPERM and anything else it cannot tell keep it alive.
func (p *pidfd) alive() bool {
	return !errors.Is(unix.PidfdSendSignal(p.fd, 0, nil, 0), unix.ESRCH)
}

func (p *pidfd) signal(sig signalNumber) error {
	return unix.PidfdSendSignal(p.fd, sig, nil, 0)
}

func (p *pidfd) pinsPid() bool { return true }
func (p *pidfd) close()        { _ = unix.Close(p.fd) }
