//go:build linux

package stop

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads the peer credentials of a Unix connection (SO_PEERCRED).
func peerUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("stop: not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var gerr error
	if err := raw.Control(func(fd uintptr) { cred, gerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, gerr
	}
	return cred.Uid, nil
}
