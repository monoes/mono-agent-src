//go:build windows

package sdconfine

import (
	"os/exec"
)

// Place refuses: there is no systemd on Windows (New never returns a confiner there).
func (c *Confiner) Place(*exec.Cmd) (func(), error) {
	return nil, unavailable(MissingPlatform, "systemd-run confinement exists on Linux only")
}
