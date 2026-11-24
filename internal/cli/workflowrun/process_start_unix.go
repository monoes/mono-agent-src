//go:build !windows

package workflowrun

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// startSlack absorbs the one-second resolution of the stored start time and
// of the kernel's boot time.
const startSlack = 5 * time.Second

// clockTicksPerSecond is USER_HZ, which is 100 on every Linux ABI we ship.
const clockTicksPerSecond = 100

// processStartTime reports when pid was started; ok=false when it cannot be
// told (the check is then skipped, the argv[0] check still applies).
func processStartTime(pid int) (time.Time, bool) {
	if runtime.GOOS == "linux" {
		return linuxProcessStart(pid)
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=").Output()
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(time.ANSIC, strings.Join(strings.Fields(string(out)), " "), time.Local)
	return t, err == nil
}

func linuxProcessStart(pid int) (time.Time, bool) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, false
	}
	// The command name is parenthesised and may hold spaces: split after the last ")".
	s := string(stat)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 { // starttime is field 22 overall, index 19 after pid and comm
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(procStat), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			boot, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			return time.Unix(boot+ticks/clockTicksPerSecond, 0), true
		}
	}
	return time.Time{}, false
}

// startedAfterExecution reports whether pid's process began after the
// execution did, which a process that wrote its own pid into the row cannot
// have: the pid was reused. An unknown time on either side is not a verdict.
func startedAfterExecution(pid int, executionStart time.Time) bool {
	if executionStart.IsZero() {
		return false
	}
	started, ok := processStartTime(pid)
	return ok && started.After(executionStart.Add(startSlack))
}
