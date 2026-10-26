//go:build !windows

package chat

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var (
	bootIDOnce sync.Once
	bootID     string
)

// bootIdentity separates boots on Linux, where the start time counts from boot.
func bootIdentity() string {
	bootIDOnce.Do(func() {
		if raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
			bootID = strings.TrimSpace(string(raw))
		}
	})
	return bootID
}

// procStatFields returns the fields of /proc/<pid>/stat after the command name:
// state first, start time at index 19.
func procStatFields(pid int) []string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil
	}
	s := string(raw)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return nil
	}
	return strings.Fields(s[i+1:])
}

// psEnv pins what ps prints to the same text whoever runs it: time zone and
// language change `lstart`.
func psOutput(args ...string) string {
	cmd := exec.Command("ps", args...)
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// pidStamp says what makes pid the process it is now: its start time (and, on
// Linux, the boot it counts from). "" when it cannot be read: the process is
// gone or is a zombie, or the lookup failed.
func pidStamp(pid int) string {
	if runtime.GOOS == "linux" {
		f := procStatFields(pid)
		if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
			return ""
		}
		return "start:" + f[19] + "@" + bootIdentity()
	}
	if s := psOutput("-o", "lstart=", "-p", strconv.Itoa(pid)); s != "" {
		return "started:" + s
	}
	return ""
}

// processState is the process state letter ("Z" for a zombie), or "" when it
// cannot be read (which callers treat as running).
func processState(pid int) string {
	if runtime.GOOS == "linux" {
		if f := procStatFields(pid); len(f) > 0 {
			return f[0]
		}
		return ""
	}
	if s := psOutput("-o", "state=", "-p", strconv.Itoa(pid)); s != "" {
		return s[:1]
	}
	return ""
}
