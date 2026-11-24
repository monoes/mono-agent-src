//go:build !windows

package proctree

import (
	"os/exec"
	"syscall"
	"time"
)

// termGrace is how long SIGTERM has to be handled before SIGKILL follows.
const termGrace = 200 * time.Millisecond

// Kill ends a started child and its descendants by signalling its process
// group, SIGTERM then SIGKILL; the child must have been started with
// Setpgid so that its pid is the group id. A no-op before Start.
func Kill(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(termGrace) // let the group clean up (temp files, profile locks) before SIGKILL
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = cmd.Process.Kill() // a child started without Setpgid has no group of its own
}
