//go:build windows

package chat

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

const hostSleepEnv = "CHAT_HOST_LOCK_SLEEP"

// TestHostLockSleepHelper is the process the tests below start: it only waits.
func TestHostLockSleepHelper(t *testing.T) {
	if os.Getenv(hostSleepEnv) == "" {
		t.Skip("helper")
	}
	time.Sleep(2 * time.Minute)
}

func hostOpenAs(pid int, rights uint32) error {
	h, err := windows.OpenProcess(rights, false, uint32(pid))
	if err == nil {
		windows.CloseHandle(h)
	}
	return err
}

// dropDebug switches SeDebugPrivilege and SeTakeOwnership off for this process while the test runs (an ssh or service session
// has them on, and they skip every access list); the state is put back.
func dropDebug(t *testing.T) {
	t.Helper()
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SeDebugPrivilege", "SeTakeOwnershipPrivilege"} {
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
			continue
		}
		off := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid}}}
		var prev windows.Tokenprivileges
		var n uint32
		if err := windows.AdjustTokenPrivileges(tok, false, &off, uint32(unsafe.Sizeof(prev)), &prev, &n); err == nil && prev.PrivilegeCount == 1 {
			p := prev
			t.Cleanup(func() { _ = windows.AdjustTokenPrivileges(tok, false, &p, 0, nil, nil) })
		}
	}
}

// The host a CLI starts is created with the locked access list, so no process of the user can open it with the right to write into
// it, start a thread in it or take a handle of it in the time before it locks itself.
func TestHostStartedByTheCLIHasTheLockedListFromItsFirstInstruction(t *testing.T) {
	dropDebug(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostLockSleepHelper$")
	cmd.Env = append(os.Environ(), hostSleepEnv+"=1")
	if err := prepareHostStart(cmd); err != nil {
		t.Fatal(err)
	}
	if err := clicore.DetachStart(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	pid := cmd.Process.Pid
	for name, r := range map[string]uint32{
		"CREATE_THREAD": windows.PROCESS_CREATE_THREAD, "VM_WRITE": windows.PROCESS_VM_WRITE, "VM_OPERATION": windows.PROCESS_VM_OPERATION,
		"DUP_HANDLE": windows.PROCESS_DUP_HANDLE, "CREATE_PROCESS": windows.PROCESS_CREATE_PROCESS, "SUSPEND_RESUME": 0x0800,
	} {
		if err := hostOpenAs(pid, r); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Errorf("the host just started opened with %s: %v", name, err)
		}
	}
	for name, r := range map[string]uint32{"TERMINATE": windows.PROCESS_TERMINATE, "QUERY_LIMITED": windows.PROCESS_QUERY_LIMITED_INFORMATION, "SYNCHRONIZE": windows.SYNCHRONIZE} {
		if err := hostOpenAs(pid, r); err != nil {
			t.Errorf("the locked host cannot be opened with %s (stop and liveness need it): %v", name, err)
		}
	}
}

// Control: a command that is not given the list starts with the default one (the test proves the list is what denies).
func TestControlAHostStartedWithoutTheListCanBeWrittenInto(t *testing.T) {
	dropDebug(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostLockSleepHelper$")
	cmd.Env = append(os.Environ(), hostSleepEnv+"=1")
	if err := clicore.DetachStart(cmd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if err := hostOpenAs(cmd.Process.Pid, windows.PROCESS_VM_WRITE|windows.PROCESS_CREATE_THREAD); err != nil {
		t.Fatalf("control: the default list should allow it: %v", err)
	}
}
