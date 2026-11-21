//go:build windows

package procresume

import "golang.org/x/sys/windows"

var procNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// Resume resumes every thread of the process h names (a process created with CREATE_SUSPENDED has exactly one, its main
// thread). h needs PROCESS_SUSPEND_RESUME.
func Resume(h windows.Handle) error {
	if err := procNtResumeProcess.Find(); err != nil {
		return err
	}
	r, _, _ := procNtResumeProcess.Call(uintptr(h))
	if st := windows.NTStatus(r); st != windows.STATUS_SUCCESS {
		return st
	}
	return nil
}
