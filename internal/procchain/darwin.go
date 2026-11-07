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

// procStamp identifies a process across pid reuse: its start time in microseconds since the epoch
// (kp_proc.p_starttime). "" when it cannot be read, and for a zombie, which is not a running process.
func procStamp(pid int) string {
	k, ok := kinfo(pid)
	if !ok || k.Proc.P_stat == 5 /* SZOMB */ {
		return ""
	}
	t := k.Proc.P_starttime
	if t.Sec == 0 && t.Usec == 0 {
		return ""
	}
	return "start:" + strconv.FormatInt(int64(t.Sec)*1_000_000+int64(t.Usec), 10)
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
