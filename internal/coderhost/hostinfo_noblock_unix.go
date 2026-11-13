//go:build !windows

package coderhost

import "syscall"

// openNoBlock makes opening a pipe that was swapped in return at once instead of waiting for a writer.
const openNoBlock = syscall.O_NONBLOCK
