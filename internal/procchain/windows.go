//go:build windows

package procchain

import (
	"strconv"
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

// bootID is the time this boot began (SYSTEM_TIMEOFDAY_INFORMATION.BootTime, the first field), so that a process of a later boot with the
// same number is told apart even by a stamp that was recorded without a creation time to compare. "" when it cannot be read. The kernel moves
// the boot time when the clock is stepped, which makes a stored stamp compare unequal: a refusal, never a wrong accept.
func bootID() string {
	var info struct {
		BootTime, CurrentTime, TimeZoneBias int64
		TimeZoneID, Reserved                uint32
		BootTimeBias, SleepTimeBias         uint64
	}
	var n uint32
	if err := windows.NtQuerySystemInformation(windows.SystemTimeOfDayInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), &n); err != nil || info.BootTime <= 0 {
		return ""
	}
	return "boot-" + strconv.FormatInt(info.BootTime, 10)
}
