//go:build windows

package procbind

import (
	"io"
	"strconv"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RESIDUAL, pinned so that nobody mistakes it for covered (SECURITY.md): the tools a worker starts keep the default list, so
// one of them can still be named as a parent by a same-user process, and the child then passes the walk as that tool's descendant.
func TestKnownGapATokenDefaultAccessListLeavesAToolOfTheWorkerClaimable(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r, in := spawnLockedIn(t, "kid")
	io.WriteString(in, "go\n")
	kid, err := strconv.Atoi(readLine(t, r, "KID "))
	if err != nil {
		t.Fatal(err)
	}
	if k, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(kid)); err == nil { // the worker's tool outlives it
		t.Cleanup(func() { _ = windows.TerminateProcess(k, 1); windows.CloseHandle(k) })
	}
	w, err := OpenWorkerHandle(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	s := startService(t, w, nil)
	ph, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(kid))
	if err != nil {
		t.Fatalf("a tool of the worker can no longer be named as a parent (%v): the gap may be closed; update SECURITY.md and this test", err)
	}
	defer windows.CloseHandle(ph)
	if got := claimAsParent(t, s, ph); got != "VERDICT allow" {
		t.Fatalf("the claim of a tool as the parent was refused (%q): the gap may be closed; update SECURITY.md and this test", got)
	}
}

// RESIDUAL: only the main thread of the worker is created with the locked list; the threads the worker makes later take the
// default one, whose set-context right is what the thread-hijack injections need. The main thread is locked.
func TestKnownGapOtherThreadsOfALockedWorkerKeepTheDefaultAccessList(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawnLocked(t, "sleep")
	readLine(t, r, "READY")
	const hijack = 0x0002 | 0x0008 | 0x0010 // THREAD_SUSPEND_RESUME | GET_CONTEXT | SET_CONTEXT
	ids := threadsOf(t, cmd.Process.Pid)
	if len(ids) < 2 {
		t.Skipf("the worker has %d thread(s)", len(ids))
	}
	main := ids[0]
	for _, id := range ids {
		if threadCreated(t, id) < threadCreated(t, main) {
			main = id
		}
	}
	if h, err := windows.OpenThread(hijack, false, main); err == nil {
		windows.CloseHandle(h)
		t.Fatal("the main thread of the locked worker was opened for set-context")
	} else if !isDenied(err) {
		t.Fatalf("the main thread: %v", err)
	}
	open := 0
	for _, id := range ids {
		if id == main {
			continue
		}
		if h, err := windows.OpenThread(hijack, false, id); err == nil {
			windows.CloseHandle(h)
			open++
		}
	}
	if open == 0 {
		t.Fatal("no other thread of the worker can be opened for set-context: the gap may be closed; update SECURITY.md and this test")
	}
}

func threadsOf(t *testing.T, pid int) []uint32 {
	t.Helper()
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snap)
	e := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	var ids []uint32
	for err := windows.Thread32First(snap, &e); err == nil; err = windows.Thread32Next(snap, &e) {
		if e.OwnerProcessID == uint32(pid) {
			ids = append(ids, e.ThreadID)
		}
	}
	return ids
}

func threadCreated(t *testing.T, id uint32) uint64 {
	t.Helper()
	h, err := windows.OpenThread(0x0800, false, id) // THREAD_QUERY_LIMITED_INFORMATION
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if r, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetThreadTimes").Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(unsafe.Pointer(&e)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u))); r == 0 {
		t.Fatal(err)
	}
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime)
}

// RESIDUAL: an elevated process can enable SeDebugPrivilege, which skips every access list. Only an elevated administrator
// has it, which is the class of attacker the Linux and macOS binding does not stop either (root).
func TestKnownGapAnElevatedProcessWithDebugPrivilegeOpensALockedWorker(t *testing.T) {
	withoutDebugPrivileges(t)
	cmd, r := spawnLocked(t, "sleep")
	readLine(t, r, "READY")
	const inject = windows.PROCESS_CREATE_THREAD | windows.PROCESS_VM_WRITE | windows.PROCESS_VM_OPERATION
	if err := openAs(cmd.Process.Pid, inject); !isDenied(err) {
		t.Fatalf("without SeDebugPrivilege the locked worker opened for injection: %v", err)
	}
	if !setPrivilege(t, "SeDebugPrivilege", true) {
		t.Skip("this process does not hold SeDebugPrivilege (it is not an elevated administrator)")
	}
	if err := openAs(cmd.Process.Pid, inject); err != nil {
		t.Fatalf("a process with SeDebugPrivilege enabled cannot open the locked worker (%v): the residual may be gone; update SECURITY.md and this test", err)
	}
}
