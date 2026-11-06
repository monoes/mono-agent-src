package stop

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// The Windows peer check of the emergency channel, split so its decisions run on every OS. Windows has no
// SO_PEERCRED: the peer of a Unix-domain socket is identified by its process id (SIO_AF_UNIX_GETPEERPID), and the
// peer is accepted only when that process runs as the same user (token user SID) as this host. The real calls
// (peercred_windows.go) sit behind winPeerOS.

type winPeerOS interface {
	// PeerPID is the process id on the other end of c.
	PeerPID(c net.Conn) (uint32, error)
	// UserSID is the token user SID (string form) of the process; an error when the token cannot be read.
	UserSID(pid uint32) (string, error)
	// SelfSID is the token user SID of this process.
	SelfSID() (string, error)
}

// peerUIDVia answers like the Unix peerUID for the emergency channel: it returns the uid the server compares with
// EmergencyServer.UID (the host's own, which is what os.Getuid reports: -1 on Windows, the same on both sides) when
// the peer is the same user, and an error otherwise. Every failure is an error: the peer is never accepted on a
// doubt.
func peerUIDVia(o winPeerOS, c net.Conn) (uint32, error) {
	self, err := o.SelfSID()
	if err != nil || self == "" {
		return 0, fmt.Errorf("stop: this process's user cannot be read: %v", err)
	}
	pid, err := o.PeerPID(c)
	if err != nil {
		return 0, fmt.Errorf("stop: the peer process cannot be identified: %w", err)
	}
	if pid == 0 {
		return 0, errors.New("stop: the peer process id is 0")
	}
	peer, err := o.UserSID(pid)
	if err != nil || peer == "" {
		return 0, fmt.Errorf("stop: the peer's user cannot be read: %v", err)
	}
	if peer != self {
		return 0, errors.New("stop: the peer runs as another user")
	}
	return uint32(os.Getuid()), nil
}
