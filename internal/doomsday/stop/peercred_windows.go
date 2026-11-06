//go:build windows

package stop

import (
	"errors"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NOT VERIFIED on a real Windows host: this file compiles (GOOS=windows go vet) and the decisions around it are
// tested through winPeerOS on Linux; SIO_AF_UNIX_GETPEERPID and the token calls have not run here. It needs the
// Unix-domain sockets of Windows 10 1803 / Server 2019 and the peer-pid ioctl of 1809; on an older Windows the ioctl
// fails and every peer is refused.

// sioAFUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID (_WSAIOR(IOC_VENDOR, 256)).
const sioAFUnixGetPeerPID = 0x58000100

func peerUID(c net.Conn) (uint32, error) { return peerUIDVia(realWinPeer{}, c) }

type realWinPeer struct{}

func (realWinPeer) PeerPID(c net.Conn) (uint32, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("stop: not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid uint32
	var ioerr error
	if err := raw.Control(func(fd uintptr) {
		// The bytes-returned count is 0 on a real Windows 10 host even though the pid is right (observed), so it is not
		// checked: the pid itself must be non-zero (peerUIDVia refuses 0) and its owner's SID must be this user's.
		var n uint32
		ioerr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	}); err != nil {
		return 0, err
	}
	return pid, ioerr
}

func (realWinPeer) UserSID(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	return tokenUserSID(tok)
}

func (realWinPeer) SelfSID() (string, error) { return tokenUserSID(windows.GetCurrentProcessToken()) }

func tokenUserSID(t windows.Token) (string, error) {
	u, err := t.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
