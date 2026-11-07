//go:build darwin

package procchain

import (
	"strconv"

	"golang.org/x/sys/unix"
)

// Supported: process ancestry can be read, so a token can be bound to a process.
const Supported = true

// kinfo reads one process from the kernel's table (sysctl kern.proc.pid). It fails for a pid that is
// not there, so an unreadable process is never taken for a readable one.
func kinfo(pid int) (*unix.KinfoProc, bool) {
	if pid <= 0 {
		return nil, false
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k == nil {
		return nil, false
	}
	return k, true
}

// Entry is one process of the kernel's table as the callers that pin processes read it.
type Entry struct {
	Ppid   int
	Start  uint64 // microseconds since the epoch (kp_proc.p_starttime)
	Zombie bool   // exited, not yet waited for: not a running process
}

const stateZombie = 5 // SZOMB, kp_proc.p_stat

// EntryOf reads a table entry. ok is false for a missing entry and for one with no start time, which identifies nothing.
// This is the one place the zombie state and the start time are read; the stamp here and the process handles of
// internal/procbind and internal/cli/chat all go through it.
func EntryOf(k *unix.KinfoProc) (e Entry, ok bool) {
	if k == nil {
		return Entry{}, false
	}
	t := k.Proc.P_starttime
	if t.Sec <= 0 || t.Usec < 0 || t.Usec >= 1_000_000 { // not a time the kernel could have stamped a process with
		return Entry{}, false
	}
	return Entry{Ppid: int(k.Eproc.Ppid), Start: uint64(t.Sec)*1_000_000 + uint64(t.Usec), Zombie: k.Proc.P_stat == stateZombie}, true
}

// ReadEntry reads the table entry of pid; ok is false when there is none.
func ReadEntry(pid int) (Entry, bool) {
	k, ok := kinfo(pid)
	if !ok {
		return Entry{}, false
	}
	return EntryOf(k)
}

// procStamp identifies a process across pid reuse: its start time. "" when it cannot be read, and for a zombie.
func procStamp(pid int) string {
	e, ok := ReadEntry(pid)
	if !ok || e.Zombie {
		return ""
	}
	return "start:" + strconv.FormatUint(e.Start, 10)
}

func parentPID(pid int) (int, bool) {
	k, ok := kinfo(pid)
	if !ok {
		return 0, false
	}
	return int(k.Eproc.Ppid), true
}

// bootID is the boot session of this boot (kern.bootsessionuuid; "" when it cannot be read).
func bootID() string {
	s, err := unix.Sysctl("kern.bootsessionuuid")
	if err != nil {
		return ""
	}
	return s
}
