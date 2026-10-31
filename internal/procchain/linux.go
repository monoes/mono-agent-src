//go:build linux

package procchain

import (
	"bytes"
	"os"
	"strconv"
)

// Supported: process ancestry can be read, so a token can be bound to a process.
const Supported = true

// procStamp identifies a process across pid reuse: its start time in clock ticks since boot
// (field 22 of /proc/<pid>/stat). "" when it cannot be read.
func procStamp(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// the command name (field 2) is in parentheses and may hold spaces: count from the last ')'
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return ""
	}
	f := bytes.Fields(b[i+1:])
	if len(f) < 20 { // state is field 3; starttime is field 22, the 20th after the name
		return ""
	}
	return "start:" + string(f[19])
}

func parentPID(pid int) (int, bool) {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range bytes.Split(status, []byte{'\n'}) {
		if v, ok := bytes.CutPrefix(line, []byte("PPid:")); ok {
			p, err := strconv.Atoi(string(bytes.TrimSpace(v)))
			return p, err == nil
		}
	}
	return 0, false
}

// bootID is the kernel's id of this boot ("" when it cannot be read).
func bootID() string {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(b))
}
