package procbind

import "errors"

// readyProbes are what Ready asks of the machine; tests replace them.
var readyProbes = struct {
	peerPidfd func() bool
	procs     func() error
}{
	peerPidfd: PeerPidfdSupported,
	procs:     func() error { _, err := NewLinuxProcs(); return err },
}

// Ready says whether the binding service can run on this machine: the kernel hands out a pidfd for a connected
// unix socket (Linux 6.5), and this process reads its own process table (the procfs of its pid namespace). The
// answers are fixed texts: they name no path or key.
func Ready() error {
	if !readyProbes.peerPidfd() {
		return errors.New("the kernel cannot tell which process connected (it needs Linux 6.5 or later)")
	}
	if err := readyProbes.procs(); err != nil {
		return errors.New("this process cannot read its own process table (another pid namespace, or a mount over /proc)")
	}
	return nil
}
