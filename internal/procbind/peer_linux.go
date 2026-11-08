//go:build linux

package procbind

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// peerCred is what the kernel says about the other end of a unix socket: its uid and pid at connect time.
type peerCred = unix.Ucred

// peerHandle pins the peer: on Linux the pidfd the kernel gave for the connection (SO_PEERPIDFD).
func peerHandle(_ *net.UnixConn, pfd int, _ peerCred) (Handle, error) {
	return HandleFromPidfd(pfd), nil
}

// closePeerFD closes what peer returned as its descriptor when the peer is refused.
func closePeerFD(pfd int) {
	if pfd >= 0 {
		_ = unix.Close(pfd)
	}
}

// PeerPidfdSupported says whether this kernel gives a pidfd for a connected unix socket
// (SO_PEERPIDFD, Linux 6.5). Without it the feature is unavailable, not degraded.
func PeerPidfdSupported() bool {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	fd, err := unix.GetsockoptInt(fds[0], unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	if err != nil {
		return false
	}
	_ = unix.Close(fd)
	return true
}

// startTimeOf reads a process's start time; tests replace it to reap the process in the middle of the read.
var startTimeOf = readStartTime

func peerIdentity(c *net.UnixConn, lookup func(*net.UnixConn) (peerCred, int, error)) (peerKey, error) {
	cred, pfd, err := lookup(c)
	if err != nil {
		if pfd >= 0 {
			_ = unix.Close(pfd)
		}
		return peerKey{}, err
	}
	h := HandleFromPidfd(pfd)
	defer h.Close()
	if cred.Pid <= 0 {
		return peerKey{}, errors.New("procbind: the peer is not visible in this pid namespace")
	}
	pid := int(cred.Pid)
	if got, err := h.Pid(); err != nil || got != pid {
		return peerKey{}, errors.New("procbind: the peer's pidfd does not name its pid")
	}
	start, err := startTimeOf(pid)
	if err != nil {
		return peerKey{}, err
	}
	if got, err := h.Pid(); err != nil || got != pid {
		return peerKey{}, errors.New("procbind: the peer was reaped while its identity was read")
	}
	return peerKey{pid: pid, start: start, id: pidfdID(pfd)}, nil
}

// pidfdIDsUnique says whether this kernel gives every process incarnation its own pidfd inode number: pidfs
// (Linux 6.9 and later) on a 64-bit build, where the number is a unique 64-bit id. A 32-bit kernel hands out
// and frees small inode numbers as pidfd inodes come and go, so a number can be reused and names nothing.
// Tests replace it.
var pidfdIDsUnique = func() bool {
	var u unix.Utsname
	if unix.Uname(&u) != nil {
		return false
	}
	return idsUniqueOn(unix.ByteSliceToString(u.Release[:]), strconv.IntSize)
}

// idsUniqueOn is the rule behind pidfdIDsUnique for a kernel release and the build's int size in bits.
func idsUniqueOn(release string, intSize int) bool {
	return intSize == 64 && kernelAtLeast(release, 6, 9)
}

// kernelAtLeast reports whether a release string such as "7.2.5-arch1" is at least major.minor.
func kernelAtLeast(release string, major, minor int) bool {
	var maj, min int
	if n, _ := fmt.Sscanf(release, "%d.%d", &maj, &min); n != 2 {
		return false
	}
	return maj > major || (maj == major && min >= minor)
}

// pidfdID is the inode number of a pidfd where that number identifies one process incarnation, else 0.
func pidfdID(pfd int) uint64 {
	if !pidfdIDsUnique() {
		return 0
	}
	var st unix.Stat_t
	if unix.Fstat(pfd, &st) != nil {
		return 0
	}
	return st.Ino
}

// peer reads what the kernel says about the other end: its uid and pid at connect time and a pidfd
// for that process.
func peer(c *net.UnixConn) (cred unix.Ucred, pidfd int, err error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return cred, -1, err
	}
	pidfd = -1
	var e1, e2 error
	if err := rc.Control(func(fd uintptr) {
		var u *unix.Ucred
		u, e1 = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if e1 == nil {
			cred = *u
		}
		pidfd, e2 = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
	}); err != nil {
		return cred, -1, err
	}
	if e1 != nil || e2 != nil {
		if e2 == nil && pidfd >= 0 {
			_ = unix.Close(pidfd) // do not keep a pidfd for a peer that is refused
		}
		if e1 != nil {
			return cred, -1, e1
		}
		return cred, -1, fmt.Errorf("SO_PEERPIDFD: %w", e2)
	}
	return cred, pidfd, nil
}
