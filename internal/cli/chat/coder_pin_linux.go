//go:build linux

package chat

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
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

// groupMembers is the pids in process group pgid, other than pgid itself (Linux).
func groupMembers(pgid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil || n <= 0 || n == pgid {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		k := bytes.LastIndexByte(stat, ')')
		if k < 0 {
			continue
		}
		// after ')': state, ppid, pgrp
		if f := strings.Fields(string(stat[k+1:])); len(f) > 2 && f[2] == strconv.Itoa(pgid) {
			out = append(out, n)
		}
	}
	return out
}
