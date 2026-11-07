//go:build windows

package procbind

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A Windows process handle names one process incarnation: while it is open the kernel keeps the process object, so
// its number cannot be given to another process, and its creation time is read from the handle itself. The only
// place a bare number is trusted is the moment of pinning (OpenProcessHandle), and every caller of it says why the
// number is believed there: the host started the process itself, the service read it from the connection.

const (
	queryRights  = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE
	statusActive = uint32(windows.WAIT_TIMEOUT)
)

var errClosed = errors.New("procbind: the handle is closed")

// ProcHandle is an open Windows process handle with the creation time read when it was pinned.
type ProcHandle struct {
	mu      sync.Mutex
	h       windows.Handle // 0 once closed
	pid     uint32
	created uint64
}

// OpenProcessHandle pins the process that holds pid now, for reading its state. It fails when there is none, when
// it is not readable, and for a process that has already exited (its creation time is read, but it is not alive).
func OpenProcessHandle(pid int) (*ProcHandle, error) { return openProcess(pid, queryRights) }

// OpenWorkerHandle is OpenProcessHandle with the right to end the process: the handle of a turn's worker, held by the
// host that started it.
func OpenWorkerHandle(pid int) (*ProcHandle, error) {
	return openProcess(pid, queryRights|windows.PROCESS_TERMINATE)
}

func openProcess(pid int, rights uint32) (*ProcHandle, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("procbind: no process %d", pid)
	}
	h, err := windows.OpenProcess(rights, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("procbind: open process %d: %w", pid, err)
	}
	p := &ProcHandle{h: h, pid: uint32(pid)}
	created, err := creationOf(h)
	if err == nil && !p.Alive() {
		err = errors.New("the process has exited")
	}
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("procbind: process %d: %w", pid, err)
	}
	if got, gerr := windows.GetProcessId(h); gerr != nil || got != uint32(pid) {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("procbind: process %d: the handle names another process", pid)
	}
	p.created = created
	return p, nil
}

func creationOf(h windows.Handle) (uint64, error) {
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	t := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	if t == 0 {
		return 0, errors.New("no creation time")
	}
	return t, nil
}

// Alive is false once the process has exited (reaped or not) and for anything the kernel will not say.
func (p *ProcHandle) Alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return false
	}
	ev, err := windows.WaitForSingleObject(p.h, 0)
	return err == nil && ev == statusActive
}

// Pid is the process's number, which stays its own as long as this handle is open.
func (p *ProcHandle) Pid() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return 0, errClosed
	}
	return int(p.pid), nil
}

// Created is the creation time read when the process was pinned (FILETIME ticks).
func (p *ProcHandle) Created() uint64 { return p.created }

// Close is idempotent.
func (p *ProcHandle) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return nil
	}
	h := p.h
	p.h = 0
	return windows.CloseHandle(h)
}

// Dup is a second handle to the same process incarnation, with its own lifetime: the registry closes the handle it was
// given, so every entry gets its own.
func (p *ProcHandle) Dup() (Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return nil, errClosed
	}
	var d windows.Handle
	self := windows.CurrentProcess()
	if err := windows.DuplicateHandle(self, p.h, self, &d, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	return &ProcHandle{h: d, pid: p.pid, created: p.created}, nil
}

// Terminate ends the process through this handle (never by number); it needs a handle from OpenWorkerHandle.
func (p *ProcHandle) Terminate() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return errClosed
	}
	return windows.TerminateProcess(p.h, 1)
}

// UserSID is the user of the process's primary token.
func (p *ProcHandle) UserSID() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return "", errClosed
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(p.h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

// parentOf is the creating process's number as the process recorded it (it can be stale: Walk compares creation times).
func (p *ProcHandle) parentOf() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return 0, errClosed
	}
	var pbi windows.PROCESS_BASIC_INFORMATION
	var n uint32
	if err := windows.NtQueryInformationProcess(p.h, windows.ProcessBasicInformation, unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &n); err != nil {
		return 0, err
	}
	return int(pbi.InheritedFromUniqueProcessId), nil
}

// WinProcs reads the process table through handles. The zero value is usable: nothing about the table needs a check.
type WinProcs struct{}

// NewProcs is the process-table reader of this OS.
func NewProcs() (WinProcs, error) { return WinProcs{}, nil }

func (WinProcs) Open(pid int) (Handle, error) {
	h, err := OpenProcessHandle(pid)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func asProc(h Handle) (*ProcHandle, error) {
	p, ok := h.(*ProcHandle)
	if !ok {
		return nil, errors.New("procbind: not a process handle of this OS")
	}
	return p, nil
}

func (WinProcs) Parent(h Handle) (int, error) {
	p, err := asProc(h)
	if err != nil {
		return 0, err
	}
	return p.parentOf()
}

// Start is the creation time of the process: a parent must not be younger than its child.
func (WinProcs) Start(h Handle) (uint64, error) {
	p, err := asProc(h)
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == 0 {
		return 0, errClosed
	}
	return p.created, nil
}
