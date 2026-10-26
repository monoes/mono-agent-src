//go:build windows

package chat

import "syscall"

func newSessionAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
