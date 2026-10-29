//go:build linux

package procbind

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Section 8 of the design: where the service socket lives and how the directory is made and
// opened. These checks stop accidents and shrink the attack surface; they are not the
// authentication. A same-user process can still replace a socket or a directory, which is why the
// service is authenticated by the keyed answer and a hostile listener only ever causes a deny.

const (
	runDirName  = "monoagent"
	sockName    = "bind.sock"
	lockName    = "bind.lock"
	maxLeafName = 64
)

// ErrAnotherHost: the socket directory's lock is held, so a host already serves this profile.
var ErrAnotherHost = errors.New("procbind: another host holds this profile's socket directory")

var errUnsafeDir = errors.New("procbind: unsafe socket directory")

var leafRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// socketLeaf turns a profile id into one safe path component: the id itself when it is plain and
// short, else "~" and the hex of its SHA-256 (a plain id can never start with "~", so the two
// cannot collide).
func socketLeaf(profile string) string {
	if len(profile) <= maxLeafName && leafRe.MatchString(profile) {
		return profile
	}
	h := sha256.Sum256([]byte(profile))
	return "~" + hex.EncodeToString(h[:16])
}

// SocketOptions names the socket's directory. Root is the runtime root (it must exist and be a
// private directory of UID); the socket is Root/monoagent/<profile>/bind.sock.
type SocketOptions struct {
	Root    string
	Profile string
	UID     int // the owner the directories must have; zero means the caller's own uid
}

func (o SocketOptions) uid() int {
	if o.UID != 0 {
		return o.UID
	}
	return os.Getuid()
}

// ResolveRoot picks the runtime root: XDG_RUNTIME_DIR when it is an absolute path to a real
// directory of this user that no one else can enter, else <home>/.monoagent/run.
func ResolveRoot(xdg, home string, uid int) string {
	if filepath.IsAbs(xdg) {
		if fi, err := os.Lstat(xdg); err == nil && fi.IsDir() && fi.Mode().Perm()&0o077 == 0 && ownedBy(fi, uid) {
			return xdg
		}
	}
	return filepath.Join(home, ".monoagent", "run")
}

func ownedBy(fi os.FileInfo, uid int) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// openRoot opens the runtime root itself (the last component must not be a symlink) and checks it.
func openRoot(o SocketOptions) (int, error) {
	if !filepath.IsAbs(o.Root) || strings.ContainsRune(o.Root, 0) {
		return -1, fmt.Errorf("%w: the runtime root %q is not an absolute path", errUnsafeDir, o.Root)
	}
	fd, err := unix.Open(o.Root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("%w: %s: %v", errUnsafeDir, o.Root, err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	// the root must be ours and closed to others: a group or other writer could swap what is under it
	if int(st.Uid) != o.uid() || st.Mode&0o022 != 0 {
		unix.Close(fd)
		return -1, fmt.Errorf("%w: %s is not a private directory of uid %d", errUnsafeDir, o.Root, o.uid())
	}
	return fd, nil
}

// openBelow opens name under dirfd without following any symlink or leaving dirfd.
func openBelow(dirfd int, name string, flags int, mode uint64) (int, error) {
	return unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Mode:    mode,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
}

// checkDir requires fd to be a directory of uid with mode exactly 0700. A wrong one is refused,
// never repaired: someone else may have made it.
func checkDir(fd int, uid int, what string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != uid || st.Mode&0o777 != 0o700 {
		return fmt.Errorf("%w: %s must be a directory of uid %d with mode 0700", errUnsafeDir, what, uid)
	}
	return nil
}

// openChain opens (creating when create is set) the directories below the root and returns the leaf's
// descriptor; every component is checked.
func openChain(rootfd int, o SocketOptions, create bool) (int, error) {
	cur, err := unix.Dup(rootfd)
	if err != nil {
		return -1, err
	}
	for _, name := range []string{runDirName, socketLeaf(o.Profile)} {
		if create {
			if err := unix.Mkdirat(cur, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(cur)
				return -1, fmt.Errorf("%w: %s: %v", errUnsafeDir, name, err)
			}
		}
		next, err := openBelow(cur, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		unix.Close(cur)
		if err != nil {
			return -1, fmt.Errorf("%w: %s: %v", errUnsafeDir, name, err)
		}
		if err := checkDir(next, o.uid(), name); err != nil {
			unix.Close(next)
			return -1, err
		}
		cur = next
	}
	return cur, nil
}

func (o SocketOptions) validate() error {
	if o.Profile == "" {
		return errors.New("procbind: a profile is needed to place the socket")
	}
	return nil
}

// procPath names an entry of a held directory descriptor; it is how the kernel is asked to bind or
// connect relative to the directory that was checked, not to whatever the path names later.
func procPath(dirfd int, name string) string {
	return "/proc/self/fd/" + strconv.Itoa(dirfd) + "/" + name
}

// SecureListener is a unix listener on the service socket, with the directory and the lock held.
type SecureListener struct {
	*net.UnixListener
	dirfd  int
	lockfd int
}

// ListenSecure creates the socket directory (private, checked component by component), takes the
// directory's lock, replaces a stale socket and binds relative to the checked directory. It refuses
// instead of repairing anything that is not exactly as expected.
func ListenSecure(o SocketOptions) (*SecureListener, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	rootfd, err := openRoot(o)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootfd)
	dirfd, err := openChain(rootfd, o, true)
	if err != nil {
		return nil, err
	}
	fail := func(err error, fds ...int) (*SecureListener, error) {
		for _, fd := range fds {
			unix.Close(fd)
		}
		return nil, err
	}
	// the lock: one host per socket directory
	lockfd, err := openBelow(dirfd, lockName, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return fail(fmt.Errorf("%w: %s: %v", errUnsafeDir, lockName, err), dirfd)
	}
	var lst unix.Stat_t
	if err := unix.Fstat(lockfd, &lst); err != nil || lst.Mode&unix.S_IFMT != unix.S_IFREG || int(lst.Uid) != o.uid() {
		return fail(fmt.Errorf("%w: %s is not a file of uid %d", errUnsafeDir, lockName, o.uid()), lockfd, dirfd)
	}
	if err := unix.Flock(lockfd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fail(ErrAnotherHost, lockfd, dirfd)
		}
		return fail(err, lockfd, dirfd)
	}
	// holding the lock, what is at the socket name is ours to clear if (and only if) it is a socket of ours
	var st unix.Stat_t
	switch err := unix.Fstatat(dirfd, sockName, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err == nil:
		if st.Mode&unix.S_IFMT != unix.S_IFSOCK || int(st.Uid) != o.uid() {
			return fail(fmt.Errorf("%w: %s exists and is not a socket of uid %d", errUnsafeDir, sockName, o.uid()), lockfd, dirfd)
		}
		if err := unix.Unlinkat(dirfd, sockName, 0); err != nil {
			return fail(err, lockfd, dirfd)
		}
	case !errors.Is(err, unix.ENOENT):
		return fail(err, lockfd, dirfd)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: procPath(dirfd, sockName), Net: "unix"})
	if err != nil {
		return fail(err, lockfd, dirfd)
	}
	l.SetUnlinkOnClose(false) // Close removes it relative to the held directory instead
	if err := unix.Fchmodat(dirfd, sockName, 0o600, 0); err != nil {
		_ = l.Close()
		_ = unix.Unlinkat(dirfd, sockName, 0)
		return fail(err, lockfd, dirfd)
	}
	if err := unix.Fstatat(dirfd, sockName, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK || int(st.Uid) != o.uid() {
		_ = l.Close()
		_ = unix.Unlinkat(dirfd, sockName, 0)
		return fail(fmt.Errorf("%w: the bound socket is not a socket of uid %d", errUnsafeDir, o.uid()), lockfd, dirfd)
	}
	return &SecureListener{UnixListener: l, dirfd: dirfd, lockfd: lockfd}, nil
}

// Close stops listening, removes the socket from the checked directory and releases the lock.
func (l *SecureListener) Close() error {
	err := l.UnixListener.Close()
	if l.dirfd >= 0 {
		_ = unix.Unlinkat(l.dirfd, sockName, 0)
		unix.Close(l.dirfd)
		l.dirfd = -1
	}
	if l.lockfd >= 0 {
		unix.Close(l.lockfd) // closing the descriptor drops the flock
		l.lockfd = -1
	}
	return err
}

// DialPath connects to a socket path; an abstract name (a leading "@" or NUL), which no
// filesystem check applies to, is refused.
func DialPath(path string) (*net.UnixConn, error) {
	if path == "" || path[0] == '@' || path[0] == 0 || strings.ContainsRune(path, 0) {
		return nil, errors.New("procbind: abstract socket names are not used")
	}
	return DialUnix(path)
}

// DialSecure opens the socket directory the way ListenSecure made it (no symlinks, ours, mode
// 0700), checks the socket, and connects relative to the checked directory. It creates nothing. The
// checks do not authenticate the service: that is the keyed answer.
func DialSecure(o SocketOptions) (*net.UnixConn, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	rootfd, err := openRoot(o)
	if err != nil {
		return nil, err
	}
	defer unix.Close(rootfd)
	dirfd, err := openChain(rootfd, o, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, sockName, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("procbind: no service socket: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || int(st.Uid) != o.uid() {
		return nil, fmt.Errorf("%w: %s is not a socket of uid %d", errUnsafeDir, sockName, o.uid())
	}
	return DialPath(procPath(dirfd, sockName))
}
