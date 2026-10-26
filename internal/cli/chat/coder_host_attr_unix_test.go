//go:build !windows

package chat

import "syscall"

// newSessionAttr makes a test child its own process group leader, as a host-started turn is.
func newSessionAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
