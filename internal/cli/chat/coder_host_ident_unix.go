//go:build !windows

package chat

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// verifyHostProcess says whether pid is a coder host (`... coder host run`), read from the process itself. host.json is a plain
// file any process of the user can write, so a restart that nobody asked for by hand never signals a pid just because the file
// names it. A variable so a test can stand in for a host that is not one.
var verifyHostProcess = verifyHostProcessOS

func verifyHostProcessOS(pid int) error {
	args, err := processArgs(pid)
	if err != nil {
		return fmt.Errorf("the process %d could not be identified as the coder host: %w", pid, err)
	}
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "coder" && args[i+1] == "host" && args[i+2] == "run" {
			return nil
		}
	}
	return fmt.Errorf("process %d is not a coder host (host.json names it, but it is not running `coder host run`)", pid)
}

func processArgs(pid int) ([]string, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), nil
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}
