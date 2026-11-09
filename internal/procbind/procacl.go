package procbind

import "fmt"

// The access lists of the processes of a bound turn on Windows (the Linux and macOS kernels give a process's parent
// and memory no such handle, see SECURITY.md "Binding service on Windows"). A process is opened by number, and a process
// of the same user could otherwise open one with the right to name it as its parent (PROCESS_CREATE_PROCESS), to start a
// thread in it (PROCESS_CREATE_THREAD), to write into it (PROCESS_VM_WRITE, PROCESS_VM_OPERATION), to read it
// (PROCESS_VM_READ), to take a handle of it or from it (PROCESS_DUP_HANDLE) or to stop its threads (PROCESS_SUSPEND_RESUME).
// The list below is an allow-list: it names what the host, the binding service and the owner do with a process and gives
// nothing else to anyone but SYSTEM, so a right that is not listed is denied to the user, to Administrators and to
// Everyone alike. The OWNER RIGHTS entry is empty on purpose: without it the owner holds READ_CONTROL and WRITE_DAC by
// being the owner, and could write the list back. The entries are built here, in every build, so the guard test that
// reads them runs on every OS.

const (
	procTerminate        = 0x0001 // stop (doomsday stop, coder host stop, taskkill)
	procQueryInformation = 0x0400 // os.FindProcess (the host liveness probe) asks for it; it reads no memory
	procQueryLimited     = 0x1000 // the ancestry walk, the pin of a peer, the creation stamp
	procReadControl      = 0x20000
	procSynchronize      = 0x100000 // waiting for the exit

	threadTerminate     = 0x0001
	threadQueryLimited  = 0x0800
	threadReadControl   = 0x20000
	threadSynchronize   = 0x100000
	processRightsKept   = procTerminate | procQueryInformation | procQueryLimited | procReadControl | procSynchronize
	primaryThreadRights = threadTerminate | threadQueryLimited | threadReadControl | threadSynchronize
)

// lockedDescriptor is the SDDL of a protected (no inherited entries) list that gives the user the rights in mask, SYSTEM
// everything, and the owner nothing of its own.
func lockedDescriptor(userSID string, mask uint32) string {
	return fmt.Sprintf("D:P(A;;0x%x;;;%s)(A;;GA;;;SY)(A;;;;;OW)", mask, userSID)
}

func processDescriptor(userSID string) string { return lockedDescriptor(userSID, processRightsKept) }

func threadDescriptor(userSID string) string { return lockedDescriptor(userSID, primaryThreadRights) }
