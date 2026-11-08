//go:build linux

package procbind

import (
	"fmt"
	"net"
	"strconv"

	"golang.org/x/sys/unix"
)

// openBelow opens name under dirfd without following any symlink or leaving dirfd.
func openBelow(dirfd int, name string, flags int, mode uint64) (int, error) {
	return unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Mode:    mode,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

// leafFor is the directory name of the profile under the run directory: its plain id when that is safe.
func leafFor(o SocketOptions) string { return socketLeaf(o.Profile) }

// socketPath names an entry of a held directory descriptor; it is how the kernel is asked to bind or
// connect relative to the directory that was checked, not to whatever the path names later.
func socketPath(dirfd int, name string) (string, error) {
	return "/proc/self/fd/" + strconv.Itoa(dirfd) + "/" + name, nil
}

// privateSocket makes the socket file private. The socket is opened by name without following a
// symlink (O_PATH), must be a socket of uid, and only then is its mode set, through the descriptor, so
// a symlink or a file of another kind swapped in under the name after the bind is never touched. (A
// different socket of the same user swapped in is not told apart: the kernel gives a listening socket's
// descriptor no inode that matches the file's. It would only leave this host listening on an unlinked
// socket, which is a deny, and the service is authenticated by its answer.) A socket created by bind has mode 0777&^umask until this runs; the
// directory is 0700, which keeps everyone else out meanwhile.
func privateSocket(dirfd, uid int) error {
	pfd, err := openBelow(dirfd, sockName, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafeDir, sockName, err)
	}
	defer unix.Close(pfd)
	var byName unix.Stat_t
	if err := unix.Fstat(pfd, &byName); err != nil {
		return err
	}
	if byName.Mode&unix.S_IFMT != unix.S_IFSOCK || int(byName.Uid) != uid {
		return fmt.Errorf("%w: %s is not a socket of uid %d", errUnsafeDir, sockName, uid)
	}
	return unix.Chmod("/proc/self/fd/"+strconv.Itoa(pfd), 0o600)
}

func checkPeerUID(c *net.UnixConn, uid int) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var cerr error
	if err := rc.Control(func(fd uintptr) { cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if int(cred.Uid) != uid {
		return fmt.Errorf("%w: the service socket is served by uid %d, not %d", errUnsafeDir, cred.Uid, uid)
	}
	return nil
}
