//go:build windows

package procbind

import "errors"

// readyProbes are what Ready asks of the machine; tests replace it.
var readyProbes = struct{ peerPID func() error }{peerPID: probePeerPID}

// Ready says whether the binding service can run on this machine: Unix-domain sockets exist and the kernel names the
// process at the other end of one (SIO_AF_UNIX_GETPEERPID). The answers are fixed texts: they name no path or key.
func Ready() error {
	if err := readyProbes.peerPID(); err != nil {
		return errors.New("this Windows cannot tell which process connected to a local socket (it needs the peer-pid query of Windows 10 version 1809 or later)")
	}
	return nil
}

// PeerPidfdSupported is false: Linux's pidfd does not exist here; the Windows service pins the peer with a process handle.
func PeerPidfdSupported() bool { return false }
