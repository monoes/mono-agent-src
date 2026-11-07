//go:build darwin

package procbind

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// readyProbes are what Ready asks of the machine; tests replace them.
var readyProbes = struct {
	peerPid func() bool
	procs   func() error
}{
	peerPid: PeerPidfdSupported,
	procs: func() error {
		_, _, err := procInfo(os.Getpid())
		return err
	},
}

// Ready says whether the binding service can run on this machine: the kernel names the pid of a connected unix socket's
// peer (LOCAL_PEERPID) and this process reads its own entry of the process table. The answers are fixed texts.
func Ready() error {
	if !readyProbes.peerPid() {
		return errors.New("the kernel cannot tell which process connected (LOCAL_PEERPID)")
	}
	if err := readyProbes.procs(); err != nil {
		return errors.New("this process cannot read its own entry of the process table")
	}
	return nil
}

// PeerPidfdSupported says whether the kernel names the peer of a connected unix socket. The name is the Linux one
// (SO_PEERPIDFD); on macOS the facility is LOCAL_PEERPID, and there is no descriptor.
func PeerPidfdSupported() bool {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	pid, err := unix.GetsockoptInt(fds[0], unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	return err == nil && pid == os.Getpid()
}
