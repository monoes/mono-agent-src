//go:build !linux

package finish

import "os/exec"

// No network namespace outside Linux: the denial is declared only, and a finish that requires it parks.
func netDenyAvailable() bool { return false }

func denyNet(*exec.Cmd) {}
