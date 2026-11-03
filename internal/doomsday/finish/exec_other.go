//go:build windows

package finish

import "os/exec"

// The mode does not start on Windows (no containment unit), so no criterion is run there.
func setGroup(*exec.Cmd) {}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
