//go:build linux

package finish

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// Network denial (note 13.6 section 5): a criterion is started in a new, empty network namespace, which has no route and no
// interface but a loopback that is down. That needs an unprivileged user namespace, which not every kernel allows, so it is
// probed once; where it is not there the result says "declared-only" and a finish that REQUIRES enforcement parks.

var (
	probeOnce sync.Once
	probeOK   bool
)

// netDenyAvailable says whether a process can be started in a new user and network namespace here.
func netDenyAvailable() bool {
	probeOnce.Do(func() {
		for _, p := range []string{"/usr/bin/true", "/bin/true"} {
			if _, err := os.Stat(p); err != nil {
				continue
			}
			c := exec.Command(p)
			denyNet(c)
			probeOK = c.Run() == nil
			return
		}
	})
	return probeOK
}

// denyNet puts cmd in new user and network namespaces. The user id is mapped to itself, so file ownership is unchanged and
// the program has no capability beyond what it had.
func denyNet(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	a := cmd.SysProcAttr
	a.Cloneflags |= syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET
	a.UidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}}
	a.GidMappings = []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}
	a.GidMappingsEnableSetgroups = false
}
