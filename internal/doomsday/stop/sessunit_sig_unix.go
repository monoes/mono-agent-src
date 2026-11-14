//go:build !windows

package stop

import "syscall"

// sessSigStop freezes a process (the brake on the fork race before the kill).
var sessSigStop = syscall.SIGSTOP
