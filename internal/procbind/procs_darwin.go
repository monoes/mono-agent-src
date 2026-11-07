//go:build darwin

package procbind

import (
	"errors"

	"github.com/monoes/mono-agent/internal/procchain"
)

// macOS has no descriptor that names a process, so a Handle is the pid together with the start time the kernel
// reported for it when the handle was made. It is alive only while the kernel still reports that pair and the
// process is not a zombie: a pid given to another process has another start time (microseconds), so it reads as
// gone. What this cannot do, unlike a pidfd, is keep the number from being reused between a check and the next use;
// the walk brackets every read with liveness checks of the handle for that reason, and SECURITY.md states the rest.

var errGone = errors.New("procbind: the process is gone")

// procInfo reads one process from the kernel's table: its parent and its start time in microseconds since the epoch.
// A process that is not there, is a zombie, or has no start time is an error.
func procInfo(pid int) (ppid int, start uint64, err error) {
	e, ok := procchain.ReadEntry(pid)
	if !ok || e.Zombie {
		return 0, 0, errGone
	}
	return e.Ppid, e.Start, nil
}

type procHandle struct {
	pid   int
	start uint64
}

// NewProcHandle pins the process that holds pid now (by its start time). It fails when there is none.
func NewProcHandle(pid int) (Handle, error) {
	_, start, err := procInfo(pid)
	if err != nil {
		return nil, err
	}
	return &procHandle{pid: pid, start: start}, nil
}

func (h *procHandle) Alive() bool {
	_, start, err := procInfo(h.pid)
	return err == nil && start == h.start
}

func (h *procHandle) Pid() (int, error) {
	if !h.Alive() {
		return 0, errGone
	}
	return h.pid, nil
}

func (h *procHandle) Close() error { return nil }

// DarwinProcs reads the host's process table through sysctl.
type DarwinProcs struct{}

// NewProcs is the process table reader of this OS.
func NewProcs() (Procs, error) { return DarwinProcs{}, nil }

func (DarwinProcs) Open(pid int) (Handle, error) { return NewProcHandle(pid) }

func handleOf(h Handle) (*procHandle, error) {
	ph, ok := h.(*procHandle)
	if !ok {
		return nil, errors.New("procbind: not a handle of this table")
	}
	return ph, nil
}

// Parent is h's parent's number, read together with the start time so it is the parent of the process the handle names.
func (DarwinProcs) Parent(h Handle) (int, error) {
	ph, err := handleOf(h)
	if err != nil {
		return 0, err
	}
	ppid, start, err := procInfo(ph.pid)
	if err != nil || start != ph.start {
		return 0, errGone
	}
	return ppid, nil
}

func (DarwinProcs) Start(h Handle) (uint64, error) {
	ph, err := handleOf(h)
	if err != nil {
		return 0, err
	}
	if !ph.Alive() {
		return 0, errGone
	}
	return ph.start, nil
}

// CloneProcHandle is a handle of its own on the process h names (the pinned pid and start time).
func CloneProcHandle(h Handle) (Handle, error) {
	ph, err := handleOf(h)
	if err != nil {
		return nil, err
	}
	c := *ph
	return &c, nil
}
