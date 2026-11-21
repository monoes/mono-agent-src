//go:build windows

package dynorg

import "os/exec"

// killPlaced ends a command whose adoption failed (no session unit exists on Windows).
func killPlaced(cmd *exec.Cmd) error { return cmd.Process.Kill() }
