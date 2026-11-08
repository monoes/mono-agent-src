//go:build windows

package procbind

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioAFUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID (_WSAIOR(IOC_VENDOR, 256)).
const sioAFUnixGetPeerPID = 0x58000100

// peerPID asks the kernel which process is at the other end of a unix connection. The answer is the process that
// connected (or accepted), as the kernel recorded it; the request cannot name it.
func peerPID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid uint32
	var ioerr error
	if err := raw.Control(func(fd uintptr) {
		// the bytes-returned count is 0 on a real Windows host even when the pid is right (observed by the stop
		// package), so it is not used: the pid itself must be a usable one
		var n uint32
		ioerr = windows.WSAIoctl(windows.Handle(fd), sioAFUnixGetPeerPID, nil, 0, (*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	}); err != nil {
		return 0, err
	}
	if ioerr != nil {
		return 0, ioerr
	}
	if pid == 0 {
		return 0, errors.New("the kernel gave no peer pid")
	}
	return pid, nil
}

// peerInfo is a connected peer as the service knows it: a pinned process (held open, so its number cannot move to
// another process) whose user was read from the process itself.
type peerInfo struct {
	proc *ProcHandle
	sid  string
}

// peerProcess pins the process at the other end of c and reads its user. The pid is the kernel's; the process is
// opened by it and its creation time is read from the handle, so what follows names one incarnation. A process that
// is not alive, or whose user cannot be read, is refused. The caller owns proc.
func peerProcess(c *net.UnixConn) (peerInfo, error) {
	pid, err := peerPID(c)
	if err != nil {
		return peerInfo{}, fmt.Errorf("peer pid: %w", err)
	}
	if pid <= 4 || pid > 1<<30 {
		return peerInfo{}, fmt.Errorf("peer pid %d is not a user process", pid)
	}
	h, err := OpenProcessHandle(int(pid))
	if err != nil {
		return peerInfo{}, err
	}
	sid, err := h.UserSID()
	if err != nil || sid == "" {
		_ = h.Close()
		return peerInfo{}, errors.New("the peer's user cannot be read")
	}
	if !h.Alive() { // read after the pin: the process that connected may have ended before the open
		_ = h.Close()
		return peerInfo{}, errors.New("the peer is gone")
	}
	return peerInfo{proc: h, sid: sid}, nil
}

// OwnSID is the string form of the user this process runs as.
func OwnSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
