package coderhost

import (
	"os/exec"
	"runtime"
)

// sleepCmd is a process that does nothing for 30 seconds, for a test that needs a live process that is not an ancestor
// of the test: sleep, or ping where there is no sleep.
func sleepCmd() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("ping", "-n", "31", "127.0.0.1")
	}
	return exec.Command("sleep", "30")
}
