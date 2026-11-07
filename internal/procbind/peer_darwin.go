//go:build darwin

package procbind

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerCred is what the kernel says about the other end of a unix socket: its uid (LOCAL_PEERCRED) and its pid
// (LOCAL_PEERPID) at connect time. The field names are those of the Linux struct so the server reads either.
type peerCred struct {
	Pid int32
	Uid uint32
}

// peerHandle pins the peer. macOS gives no descriptor for a process, so the handle is the pid and the start time that
// process has now; it is alive only while that pair is what the kernel still reports (NewProcHandle).
func peerHandle(_ int, cred peerCred) (Handle, error) {
	if cred.Pid <= 1 {
		return nil, errors.New("procbind: the peer has no usable pid")
	}
	return NewProcHandle(int(cred.Pid))
}

// closePeerFD closes what peer returned as its descriptor: there is none on macOS.
func closePeerFD(int) {}

// peer reads what the kernel says about the other end: its uid and the pid it connected with. The descriptor result
// is always -1 (macOS has no pidfd). Anything the kernel does not say, or says in a form this code does not know, is
// an error: the peer is refused.
func peer(c *net.UnixConn) (cred peerCred, _ int, err error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return cred, -1, err
	}
	var x *unix.Xucred
	var pid int
	var e1, e2 error
	if err := rc.Control(func(fd uintptr) {
		x, e1 = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		pid, e2 = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return cred, -1, err
	}
	switch {
	case e1 != nil:
		return cred, -1, fmt.Errorf("LOCAL_PEERCRED: %w", e1)
	case e2 != nil:
		return cred, -1, fmt.Errorf("LOCAL_PEERPID: %w", e2)
	case x == nil || x.Version != 0: // XUCRED_VERSION: a structure of another version is not read
		return cred, -1, errors.New("LOCAL_PEERCRED: unexpected credential version")
	case pid <= 0 || pid > 1<<30:
		return cred, -1, errors.New("LOCAL_PEERPID: the kernel names no process")
	}
	return peerCred{Pid: int32(pid), Uid: x.Uid}, -1, nil
}

// peerIdentity reads the identity of the process at the other end, once, at accept: its pid and the start time that
// process has (microseconds). A pid the kernel hands to a new process has another start time, so it is another key.
// The handle must still be alive after the start time is read.
func peerIdentity(c *net.UnixConn, lookup func(*net.UnixConn) (peerCred, int, error)) (peerKey, error) {
	cred, fd, err := lookup(c)
	if err != nil {
		closePeerFD(fd)
		return peerKey{}, err
	}
	h, err := peerHandle(fd, cred)
	if err != nil {
		return peerKey{}, err
	}
	defer h.Close()
	ph, ok := h.(*procHandle)
	if !ok || !h.Alive() {
		return peerKey{}, errors.New("procbind: the peer exited while its identity was read")
	}
	return peerKey{pid: ph.pid, start: ph.start}, nil
}
