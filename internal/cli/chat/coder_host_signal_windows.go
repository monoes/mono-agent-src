//go:build windows

package chat

import (
	"errors"
	"os/exec"
	"strconv"
)

// terminateGroup and killGroup end the turn's process and everything it
// started (taskkill /T); Windows has no signal that asks first.
func terminateGroup(pid int) error { return killGroup(pid) }

func killGroup(pid int) error {
	if pid <= 1 {
		return errors.New("not a turn's pid")
	}
	return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
