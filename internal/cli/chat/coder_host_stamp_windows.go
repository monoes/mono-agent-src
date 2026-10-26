//go:build windows

package chat

import (
	"strconv"
	"syscall"
)

// pidStamp says what makes pid the process it is now: its creation time. ""
// when the process cannot be opened (it is gone).
func pidStamp(pid int) string {
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer syscall.CloseHandle(h)
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return ""
	}
	return "created:" + strconv.FormatInt(created.Nanoseconds(), 10)
}

// processState: Windows has no zombies; a process that exited cannot be opened.
func processState(int) string { return "" }
