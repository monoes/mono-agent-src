//go:build darwin

package finish

import "golang.org/x/sys/unix"

// kernMaxProcPerUID: macOS silently caps RLIMIT_NPROC at kern.maxprocperuid even when setrlimit accepted more.
func kernMaxProcPerUID() (uint64, bool) {
	v, err := unix.SysctlUint32("kern.maxprocperuid")
	return uint64(v), err == nil && v > 0
}
