//go:build windows

package chat

import "golang.org/x/sys/windows"

// awaitExitNoReap returns once the child pid has exited, leaving it unreaped: cmd.Process holds the child's handle until
// cmd.Wait, so the process object, and with it the number, stays this child's until then (the same property a Linux
// zombie gives). It reports whether it held the child that way; anything it cannot observe is a no.
func awaitExitNoReap(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, windows.INFINITE)
	return err == nil && ev == windows.WAIT_OBJECT_0
}
