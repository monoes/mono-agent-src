//go:build darwin

package procbind

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// maxSockPath is the longest path a unix socket address holds on macOS (sun_path is 104 bytes, one for the NUL).
const maxSockPath = 103

// openBelow opens name under dirfd without following a symlink. macOS has no openat2, so what Linux asks the kernel to
// enforce is enforced here: name is one plain component (no separator, never "." or ".."), and O_NOFOLLOW refuses it
// when it is a symlink. Every name passed is a fixed one or a socketLeaf, so a refusal here is a bug, never input.
func openBelow(dirfd int, name string, flags int, mode uint64) (int, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return -1, unix.EINVAL
	}
	return unix.Openat(dirfd, name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode))
}

// socketPath names an entry of a held directory descriptor. macOS has no /proc/self/fd, so the descriptor is asked for
// the path of its directory (F_GETPATH, which follows a rename) and the name is added. The path is looked up again by
// bind and connect, so a same-user process that swaps a parent of the directory in between can make the socket land
// elsewhere or be found elsewhere: a bound socket that is not at the checked directory is refused by privateSocket,
// and the service is authenticated by the keyed answer, so a hostile listener only ever causes a deny (as on Linux).
// A path the address cannot hold is refused.
func socketPath(dirfd int, name string) (string, error) {
	var buf [1024]byte
	if _, _, e := unix.Syscall(unix.SYS_FCNTL, uintptr(dirfd), unix.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return "", fmt.Errorf("%w: the socket directory has no path: %v", errUnsafeDir, e)
	}
	dir := unix.ByteSliceToString(buf[:])
	if dir == "" || dir[0] != '/' {
		return "", fmt.Errorf("%w: the socket directory has no path", errUnsafeDir)
	}
	p := dir + "/" + name
	if len(p) > maxSockPath {
		return "", fmt.Errorf("%w: the socket path is longer than %d bytes", errUnsafeDir, maxSockPath)
	}
	return p, nil
}

// privateSocket makes the socket file private. The name is looked up without following a symlink, must be a socket of
// uid, and only then is its mode set (again without following one). A socket created by bind has mode 0777&^umask until
// this runs; the directory is 0700, which keeps everyone else out meanwhile. A bind that landed in another directory
// than the checked one finds no socket here and is refused.
func privateSocket(dirfd, uid int) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, sockName, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafeDir, sockName, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || int(st.Uid) != uid {
		return fmt.Errorf("%w: %s is not a socket of uid %d", errUnsafeDir, sockName, uid)
	}
	if err := unix.Fchmodat(dirfd, sockName, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafeDir, sockName, err)
	}
	return nil
}

func checkPeerUID(c *net.UnixConn, uid int) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Xucred
	var cerr error
	if err := rc.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED) }); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if cred == nil || cred.Version != 0 {
		return errors.New("procbind: unexpected peer credential version")
	}
	if int(cred.Uid) != uid {
		return fmt.Errorf("%w: the service socket is served by uid %d, not %d", errUnsafeDir, cred.Uid, uid)
	}
	return nil
}
