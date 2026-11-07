//go:build darwin

package procbind

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// peerCred is what the kernel says about the other end of a unix socket: its uid (LOCAL_PEERCRED) and its pid
// (LOCAL_PEERPID, the pid the peer connected with). The field names are those of the Linux struct so the server reads either.
type peerCred struct {
	Pid int32
	Uid uint32
}

// peerHandle pins the peer. LOCAL_PEERPID is a number the kernel remembers from the connect, and macOS attaches no
// descriptor to the connection (LOCAL_PEERTOKEN is looked up from the pid when it is asked, so it names whoever holds the
// number now). What the kernel does tie to the connection is the descriptor: the peer's end of the socket is open in the
// process that holds it. So the process now at the peer's pid is accepted only if it holds that end of THIS connection (its
// socket's pcb is the one this end is connected to); a pid recycled after the connector let go of the connection does
// not. The handle is the pid and the start time, checked alive after the proof.
func peerHandle(c *net.UnixConn, _ int, cred peerCred) (Handle, error) {
	if cred.Pid <= 0 {
		return nil, errors.New("procbind: the peer has no usable pid")
	}
	h, err := NewProcHandle(int(cred.Pid))
	if err != nil {
		return nil, err
	}
	if err := holdsConnection(c, int(cred.Pid)); err != nil || !h.Alive() {
		_ = h.Close()
		return nil, errors.New("procbind: the process holding the peer's pid does not hold the peer's end of the connection")
	}
	return h, nil
}

// closePeerFD closes what peer returned as its descriptor: there is none on macOS.
func closePeerFD(int) {}

// getsockoptRaw reads a socket option of the peer into p and returns the length the kernel gave, so a short answer is seen.
func getsockoptRaw(c *net.UnixConn, opt int, p unsafe.Pointer, size uintptr) (uintptr, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	n := uint32(size)
	var errno unix.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = unix.Syscall6(unix.SYS_GETSOCKOPT, fd, unix.SOL_LOCAL, uintptr(opt), uintptr(p), uintptr(unsafe.Pointer(&n)), 0)
	}); err != nil {
		return 0, err
	}
	if errno != 0 {
		return 0, errno
	}
	return uintptr(n), nil
}

// checkXucred refuses a credential structure that is short, of another version, or untouched (a zeroed one has no groups;
// the first group is the effective gid, so a filled structure always has one).
func checkXucred(x *unix.Xucred, n uintptr) error {
	switch {
	case n != unsafe.Sizeof(*x):
		return errors.New("LOCAL_PEERCRED: short answer")
	case x.Version != 0: // XUCRED_VERSION
		return errors.New("LOCAL_PEERCRED: unexpected credential version")
	case x.Ngroups < 1:
		return errors.New("LOCAL_PEERCRED: no credential was filled in")
	}
	return nil
}

// peerXucred reads and checks the peer's LOCAL_PEERCRED.
func peerXucred(c *net.UnixConn) (*unix.Xucred, error) {
	var x unix.Xucred
	n, err := getsockoptRaw(c, unix.LOCAL_PEERCRED, unsafe.Pointer(&x), unsafe.Sizeof(x))
	if err != nil {
		return nil, fmt.Errorf("LOCAL_PEERCRED: %w", err)
	}
	if err := checkXucred(&x, n); err != nil {
		return nil, err
	}
	return &x, nil
}

// joinPeer puts the answers of the kernel together and refuses a pid that is not a process number.
func joinPeer(x *unix.Xucred, pid int) (peerCred, error) {
	if pid <= 0 || pid > 1<<30 {
		return peerCred{}, errors.New("LOCAL_PEERPID: the kernel names no process")
	}
	return peerCred{Pid: int32(pid), Uid: x.Uid}, nil
}

// peer reads what the kernel says about the other end: its uid and the pid it connected with. The
// descriptor result is always -1 (macOS has no pidfd). Anything the kernel does not say, or says in a form this code does
// not know, is an error: the peer is refused.
func peer(c *net.UnixConn) (cred peerCred, _ int, err error) {
	x, err := peerXucred(c)
	if err != nil {
		return cred, -1, err
	}
	rc, err := c.SyscallConn()
	if err != nil {
		return cred, -1, err
	}
	var pid int
	var e error
	if err := rc.Control(func(fd uintptr) { pid, e = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID) }); err != nil {
		return cred, -1, err
	}
	if e != nil {
		return cred, -1, fmt.Errorf("LOCAL_PEERPID: %w", e)
	}
	cred, err = joinPeer(x, pid)
	return cred, -1, err
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
	h, err := peerHandle(c, fd, cred)
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
