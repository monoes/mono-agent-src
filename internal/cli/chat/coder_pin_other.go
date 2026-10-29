//go:build !linux && !windows

package chat

import "syscall"

type signalNumber = syscall.Signal

// pidHandle holds only the number: there is no kernel handle here, so the caller checks the process
// again right before each signal (a pid can still be reused in the gap).
type pidHandle struct{ pid int }

func pinProcessOS(pid int) (pinnedProcess, error) {
	if !processAlive(pid) {
		return nil, errNoProcess
	}
	return &pidHandle{pid}, nil
}

func (p *pidHandle) alive() bool { return processAlive(p.pid) }

func (p *pidHandle) signal(sig signalNumber) error { return syscall.Kill(p.pid, sig) }

func (p *pidHandle) pinsPid() bool { return false }
func (p *pidHandle) close()        {}
