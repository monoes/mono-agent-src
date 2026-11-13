//go:build linux

package sdconfine

import (
	"os"
	"syscall"
)

// sandboxAttr is what finish.Sandbox sets on a command: its own process group and an empty user and network namespace.
func sandboxAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}}
}
