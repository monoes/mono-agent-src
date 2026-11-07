//go:build darwin

package procbind

import (
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/sys/unix"
)

// peerCred is what the kernel says about the other end of a unix socket: its uid (LOCAL_PEERCRED), its pid (LOCAL_PEERPID)
// and its pid version (LOCAL_PEERTOKEN, the audit token the kernel attached to the connection at connect time). The pid
// version changes whenever a pid is given to a new process or a process executes a new image, so (pid, pid version)
// names one incarnation and the pid alone does not. The field names of Uid and Pid are those of the Linux struct.
type peerCred struct {
	Pid        int32
	Uid        uint32
	Pidversion uint32
}

// peerHandle pins the peer and requires that the process now holding its pid is the one that connected: its pid version,
// read from the kernel's table, must be the one in the token the kernel attached to the connection. macOS has no pidfd,
// so the handle is the pid and the start time; a pid recycled between the connect and this call has another pid version
// and is refused.
func peerHandle(_ int, cred peerCred) (Handle, error) {
	if cred.Pid <= 1 || cred.Pidversion == 0 {
		return nil, errors.New("procbind: the peer has no usable identity")
	}
	h, err := NewProcHandle(int(cred.Pid))
	if err != nil {
		return nil, err
	}
	v, err := procIdversion(int(cred.Pid))
	if err != nil || v != cred.Pidversion || !h.Alive() {
		_ = h.Close()
		return nil, errors.New("procbind: the process holding the peer's pid is not the one that connected")
	}
	return h, nil
}

// closePeerFD closes what peer returned as its descriptor: there is none on macOS.
func closePeerFD(int) {}

const (
	localPeerToken = 6 // LOCAL_PEERTOKEN
	tokenWords     = 8 // audit_token_t: uid, euid, egid, ruid, rgid, pid, asid, pidversion
)

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

// joinPeer puts the three answers of the kernel together and refuses any that is missing or disagrees with another: the pid
// must be a process number, and the token must name that pid and that effective uid and carry a pid version.
func joinPeer(x *unix.Xucred, pid int, tok [tokenWords]uint32, tokLen uintptr) (peerCred, error) {
	switch {
	case pid <= 0 || pid > 1<<30:
		return peerCred{}, errors.New("LOCAL_PEERPID: the kernel names no process")
	case tokLen != unsafe.Sizeof(tok):
		return peerCred{}, errors.New("LOCAL_PEERTOKEN: short answer")
	case int(tok[5]) != pid:
		return peerCred{}, errors.New("LOCAL_PEERTOKEN: names another process than LOCAL_PEERPID")
	case tok[1] != x.Uid:
		return peerCred{}, errors.New("LOCAL_PEERTOKEN: names another user than LOCAL_PEERCRED")
	case tok[7] == 0:
		return peerCred{}, errors.New("LOCAL_PEERTOKEN: no pid version")
	}
	return peerCred{Pid: int32(pid), Uid: x.Uid, Pidversion: tok[7]}, nil
}

// peer reads what the kernel says about the other end: its uid, the pid it connected with and its audit token. The
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
	var tok [tokenWords]uint32
	n, err := getsockoptRaw(c, localPeerToken, unsafe.Pointer(&tok[0]), unsafe.Sizeof(tok))
	if err != nil {
		return cred, -1, fmt.Errorf("LOCAL_PEERTOKEN: %w", err)
	}
	cred, err = joinPeer(x, pid, tok, n)
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
