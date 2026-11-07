//go:build windows

package procchain

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supported: process ancestry can be read, so a token can be bound to a process.
const Supported = true

// readProc reads one process through a handle it opens and closes itself. Opening is by number, which is why the
// decisions in winlogic.go never trust a number alone: the creation time read from the handle is the process's own.
func readProc(pid int) (procInfo, error) {
	if pid <= 0 {
		return procInfo{}, errUnreadable
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return procInfo{}, errUnreadable
	}
	defer windows.CloseHandle(h)
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return procInfo{}, errUnreadable
	}
	// the process object's own state tells an exit apart (an exit code can equal STILL_ACTIVE)
	ev, err := windows.WaitForSingleObject(h, 0)
	if err != nil || (ev != windows.WAIT_OBJECT_0 && ev != uint32(windows.WAIT_TIMEOUT)) {
		return procInfo{}, errUnreadable
	}
	var pbi windows.PROCESS_BASIC_INFORMATION
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), &n); err != nil {
		return procInfo{}, errUnreadable
	}
	return procInfo{
		parent:  int(pbi.InheritedFromUniqueProcessId),
		created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime),
		exited:  ev == windows.WAIT_OBJECT_0,
	}, nil
}

func procStamp(pid int) string { return stampOf(readProc, pid) }

func parentPID(pid int) (int, bool) { return parentOf(readProc, pid) }

// bootID is "": a creation time is an absolute FILETIME, so a process of a later boot cannot share a stamp, and the kernel's boot
// time moves when the clock is stepped, which would make a live turn's recorded stamp stop matching it.
func bootID() string { return "" }
