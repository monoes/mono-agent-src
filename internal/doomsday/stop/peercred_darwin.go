//go:build darwin

package stop

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID reads the peer credentials of a Unix connection (LOCAL_PEERCRED). NOT VERIFIED on a real macOS host.
func peerUID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("stop: not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Xucred
	var gerr error
	if err := raw.Control(func(fd uintptr) { cred, gerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED) }); err != nil {
		return 0, err
	}
	if gerr != nil {
		return 0, gerr
	}
	if cred.Version != 0 { // XUCRED_VERSION: a structure of another version is not read
		return 0, errors.New("stop: unexpected peer credential version")
	}
	return cred.Uid, nil
}
