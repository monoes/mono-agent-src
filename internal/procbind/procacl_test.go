package procbind

import (
	"strings"
	"testing"
)

// The lists of procacl.go are what keep a same-user process from naming a turn's worker as its parent or writing into it on
// Windows; the kernel checks them there (procacl_windows_test.go), these tests keep the lists themselves from being widened
// on any OS.

const (
	pCreateThread  = 0x0002
	pSetSessionID  = 0x0004
	pVMOperation   = 0x0008
	pVMRead        = 0x0010
	pVMWrite       = 0x0020
	pDupHandle     = 0x0040
	pCreateProcess = 0x0080
	pSetQuota      = 0x0100
	pSetInfo       = 0x0200
	pSuspendResume = 0x0800
	pSetLimited    = 0x2000
	stdDelete      = 0x10000
	stdWriteDAC    = 0x40000
	stdWriteOwner  = 0x80000
	genericAll     = 0x10000000
	genericWrite   = 0x40000000
	genericExec    = 0x20000000
	genericRead    = 0x80000000
)

func TestTheWorkersProcessListGivesNoRightToReachIntoIt(t *testing.T) {
	danger := map[string]uint32{
		"CREATE_THREAD": pCreateThread, "SET_SESSIONID": pSetSessionID, "VM_OPERATION": pVMOperation, "VM_READ": pVMRead,
		"VM_WRITE": pVMWrite, "DUP_HANDLE": pDupHandle, "CREATE_PROCESS": pCreateProcess, "SET_QUOTA": pSetQuota,
		"SET_INFORMATION": pSetInfo, "SUSPEND_RESUME": pSuspendResume, "SET_LIMITED_INFORMATION": pSetLimited,
		"DELETE": stdDelete, "WRITE_DAC": stdWriteDAC, "WRITE_OWNER": stdWriteOwner,
		"GENERIC_ALL": genericAll, "GENERIC_WRITE": genericWrite, "GENERIC_EXECUTE": genericExec, "GENERIC_READ": genericRead,
	}
	for name, bit := range danger {
		if processRightsKept&bit != 0 {
			t.Errorf("the process list gives %s", name)
		}
	}
	if processRightsKept != 0x121401 {
		t.Errorf("the process list is %#x, want terminate, query, query-limited, read-control and synchronize (0x121401)", processRightsKept)
	}
}

func TestThePrimaryThreadListGivesNoRightToHijackIt(t *testing.T) {
	// THREAD_SUSPEND_RESUME 0x2, GET_CONTEXT 0x8, SET_CONTEXT 0x10, SET_INFORMATION 0x20, SET_THREAD_TOKEN 0x80,
	// IMPERSONATE 0x100, DIRECT_IMPERSONATION 0x200, SET_LIMITED_INFORMATION 0x400
	for _, bit := range []uint32{0x2, 0x8, 0x10, 0x20, 0x80, 0x100, 0x200, 0x400, stdWriteDAC, stdWriteOwner, genericAll, genericWrite} {
		if primaryThreadRights&bit != 0 {
			t.Errorf("the thread list gives %#x", bit)
		}
	}
	if primaryThreadRights != 0x120801 {
		t.Errorf("the thread list is %#x, want terminate, query-limited, read-control and synchronize (0x120801)", primaryThreadRights)
	}
}

func TestTheDescriptorNamesTheUserSystemAndAnEmptyOwnerEntryOnly(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"
	for _, d := range []string{processDescriptor(sid), threadDescriptor(sid)} {
		if !strings.HasPrefix(d, "D:P(") { // protected: nothing is inherited into the list
			t.Errorf("%s: not a protected list", d)
		}
		aces := strings.Split(strings.TrimSuffix(strings.TrimPrefix(d, "D:P("), ")"), ")(")
		if len(aces) != 3 {
			t.Fatalf("%s: %d entries, want the user, SYSTEM and OWNER RIGHTS", d, len(aces))
		}
		want := []string{";;" + sid, ";;SY", ";;OW"}
		for i, a := range aces {
			f := strings.Split(a, ";")
			if len(f) != 6 || f[0] != "A" || f[5] != strings.TrimPrefix(want[i], ";;") {
				t.Errorf("%s: entry %d is %q", d, i, a)
			}
		}
		if last := strings.Split(aces[2], ";"); last[2] != "" {
			t.Errorf("%s: the OWNER RIGHTS entry holds rights %q: the owner could write the list back", d, last[2])
		}
	}
}
