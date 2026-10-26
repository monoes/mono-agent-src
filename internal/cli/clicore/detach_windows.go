//go:build windows

package clicore

import (
	"os/exec"
	"syscall"
)

const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

// DetachStart starts cmd detached from this console, without a window, so
// it outlives this process and the app that ran it.
func DetachStart(cmd *exec.Cmd) error {
	attr := &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedProcess | createNewProcessGroup | createNoWindow}
	if cmd.SysProcAttr != nil {
		attr.AdditionalInheritedHandles = cmd.SysProcAttr.AdditionalInheritedHandles // handles the caller passes on
	}
	cmd.SysProcAttr = attr
	return cmd.Start()
}
