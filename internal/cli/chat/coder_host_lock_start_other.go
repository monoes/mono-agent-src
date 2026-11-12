//go:build !windows

package chat

import "os/exec"

// prepareHostStart: only Windows gives a process its access list at creation (see coder_host_lock_start_windows.go).
func prepareHostStart(*exec.Cmd) error { return nil }
