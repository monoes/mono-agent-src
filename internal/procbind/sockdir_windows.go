//go:build windows

package procbind

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The socket directory on Windows (section 8 of the design). The directory chain below the runtime root is made with
// a protected DACL that grants the user and SYSTEM only, every component is opened without following a reparse point
// and checked (a directory, owned by the user or the Administrators, a DACL that allows nobody else), and the handles
// stay open while the service lives: a directory with an open handle that shares no delete cannot be renamed or
// removed, so the checked directory is the one the socket is bound in. These checks stop accidents and shrink the
// attack surface; they are not the authentication (the keyed answer is).

// maxSockPath is what a Windows unix socket address holds (sun_path is 108 bytes, with the terminating NUL).
const maxSockPath = 107

var errPathTooLong = errors.New("procbind: the socket path is longer than a Windows unix socket address can hold (107 bytes): use a shorter home folder or runtime root")

// SocketOptions names the socket's directory. Root is the runtime root (a private directory of the user); the socket
// is Root\monoagent\<profile>\bind.sock.
type SocketOptions struct {
	Root    string
	Profile string
	UID     int // unused here: the owner is the user of this process
	// CreateRoot makes the last component of Root when it does not exist; its parent must.
	CreateRoot bool
}

// ResolveRoot is the runtime root: <home>\.monoagent\run (there is no XDG_RUNTIME_DIR here).
func ResolveRoot(_, home string, _ int) string { return filepath.Join(home, ".monoagent", "run") }

func (o SocketOptions) validate() error {
	if o.Profile == "" {
		return errors.New("procbind: a profile is needed to place the socket")
	}
	return nil
}

// trusted is the set of principals a checked object may be owned by or grant access to.
type trusted map[string]bool

func trustedSet() (trusted, string, error) {
	me, err := OwnSID()
	if err != nil {
		return nil, "", err
	}
	// the user, SYSTEM and the Administrators (an elevated token owns what it creates as the Administrators group)
	return trusted{me: true, "S-1-5-18": true, "S-1-5-32-544": true, "S-1-3-0": true}, me, nil
}

func (o SocketOptions) cleanRoot() (string, error) {
	r := o.Root
	if !filepath.IsAbs(r) || strings.ContainsRune(r, 0) || strings.Contains(r, `\\.\`) || strings.Contains(r, `\\?\`) {
		return "", fmt.Errorf("%w: the runtime root %q is not an absolute path", errUnsafeDir, o.Root)
	}
	r = filepath.Clean(r)
	if i := strings.IndexByte(r[min(len(r), 2):], ':'); i >= 0 { // a stream name after the drive
		return "", fmt.Errorf("%w: the runtime root %q names a stream", errUnsafeDir, o.Root)
	}
	return r, nil
}

// privateSA is a security descriptor that grants the user and SYSTEM full control and nobody else, not inheriting.
func privateSA(me string) (*windows.SecurityAttributes, error) {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + me + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

// openDir opens a directory without following a reparse point, and checks it. It asks for the right to list it (an open
// that asks only for attributes is exempt from the sharing rules) and shares read and write but not delete, so while the
// handle is open the directory cannot be renamed or removed.
func openDir(path string, ok trusted) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("%w: %s: %v", errUnsafeDir, filepath.Base(path), err)
	}
	var bi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &bi); err != nil || bi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || bi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(h)
		return windows.InvalidHandle, fmt.Errorf("%w: %s is not a plain directory", errUnsafeDir, filepath.Base(path))
	}
	if err := checkAccess(h, ok); err != nil {
		_ = windows.CloseHandle(h)
		return windows.InvalidHandle, fmt.Errorf("%w: %s: %v", errUnsafeDir, filepath.Base(path), err)
	}
	return h, nil
}

// checkAccess requires the object to be owned by a trusted principal and its DACL to allow only trusted principals.
// A missing DACL (everyone) and any ACE that is not a plain allow or deny are refused.
func checkAccess(h windows.Handle, ok trusted) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return accessOK(sd, ok)
}

func accessOK(sd *windows.SECURITY_DESCRIPTOR, ok trusted) error {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !ok[owner.String()] {
		return errors.New("it is owned by someone else")
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return errors.New("it has no access list, so everyone has access")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return errors.New("its access list cannot be read")
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); !ok[sid.String()] {
				return errors.New("its access list admits someone else")
			}
		default:
			return errors.New("its access list has an entry that is neither allow nor deny")
		}
	}
	return nil
}

// chain is the held directories of the socket path, root first.
type chain struct {
	handles []windows.Handle
	dir     string // the leaf directory
}

func (c *chain) close() {
	for _, h := range c.handles {
		_ = windows.CloseHandle(h)
	}
	c.handles = nil
}

// openChain opens (creating when create is set) the root, the runtime folder and the profile's folder.
func openChain(o SocketOptions, create bool) (*chain, error) {
	root, err := o.cleanRoot()
	if err != nil {
		return nil, err
	}
	ok, me, err := trustedSet()
	if err != nil {
		return nil, err
	}
	sa, err := privateSA(me)
	if err != nil {
		return nil, err
	}
	c := &chain{}
	step := func(path string, mk bool) error {
		if mk {
			p, perr := windows.UTF16PtrFromString(path)
			if perr != nil {
				return perr
			}
			if cerr := windows.CreateDirectory(p, sa); cerr != nil && !errors.Is(cerr, windows.ERROR_ALREADY_EXISTS) {
				return fmt.Errorf("%w: %s: %v", errUnsafeDir, filepath.Base(path), cerr)
			}
		}
		h, oerr := openDir(path, ok)
		if oerr != nil {
			return oerr
		}
		c.handles = append(c.handles, h)
		return nil
	}
	if err := step(root, create && o.CreateRoot); err != nil {
		c.close()
		return nil, err
	}
	dir := root
	for _, name := range []string{runDirName, socketLeaf(o.Profile)} {
		dir = filepath.Join(dir, name)
		if err := step(dir, create); err != nil {
			c.close()
			return nil, err
		}
	}
	c.dir = dir
	return c, nil
}

func sockPath(dir string) (string, error) {
	p := filepath.Join(dir, sockName)
	if len(p) > maxSockPath {
		return "", errPathTooLong
	}
	return p, nil
}

// SecureListener is a unix listener on the service socket, with the directory chain and the lock held.
type SecureListener struct {
	l    *net.UnixListener
	once sync.Once
	ch   *chain
	lock windows.Handle
	path string
}

// AcceptUnix accepts a connection; a SecureListener is what Server.Serve takes.
func (s *SecureListener) AcceptUnix() (*net.UnixConn, error) { return s.l.AcceptUnix() }

// Addr is the listener's address.
func (s *SecureListener) Addr() net.Addr { return s.l.Addr() }

// ListenSecure creates the socket directory (private, checked component by component), takes the directory's lock,
// replaces a stale socket and binds in the held directory. It refuses instead of repairing anything that is not
// exactly as expected.
func ListenSecure(o SocketOptions) (*SecureListener, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	ch, err := openChain(o, true)
	if err != nil {
		return nil, err
	}
	path, err := sockPath(ch.dir)
	if err != nil {
		ch.close()
		return nil, err
	}
	// the lock: one host per socket directory; the file is opened sharing nothing, so a second open is refused
	lp, err := windows.UTF16PtrFromString(filepath.Join(ch.dir, lockName))
	if err != nil {
		ch.close()
		return nil, err
	}
	_, me, _ := trustedSet()
	sa, err := privateSA(me)
	if err != nil {
		ch.close()
		return nil, err
	}
	lock, err := windows.CreateFile(lp, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, sa, windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		ch.close()
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, ErrAnotherHost
		}
		return nil, fmt.Errorf("%w: %s: %v", errUnsafeDir, lockName, err)
	}
	fail := func(err error) (*SecureListener, error) {
		_ = windows.CloseHandle(lock)
		ch.close()
		return nil, err
	}
	var bi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(lock, &bi); err != nil || bi.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return fail(fmt.Errorf("%w: %s is not a plain file", errUnsafeDir, lockName))
	}
	// holding the lock, what is at the socket name is ours to clear if (and only if) it is a socket
	switch fi, err := os.Lstat(path); {
	case err == nil:
		if fi.Mode()&os.ModeSocket == 0 {
			return fail(fmt.Errorf("%w: %s exists and is not a socket", errUnsafeDir, sockName))
		}
		if err := os.Remove(path); err != nil {
			return fail(err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return fail(err)
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fail(err)
	}
	l.SetUnlinkOnClose(false) // Close removes it itself
	if err := checkSocketFile(path); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return fail(err)
	}
	return &SecureListener{l: l, ch: ch, lock: lock, path: path}, nil
}

// checkSocketFile requires the socket file to be a socket whose access list admits only the trusted principals.
func checkSocketFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %s is not a socket", errUnsafeDir, sockName)
	}
	ok, _, err := trustedSet()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafeDir, sockName, err)
	}
	if err := accessOK(sd, ok); err != nil {
		return fmt.Errorf("%w: %s: %v", errUnsafeDir, sockName, err)
	}
	return nil
}

// Close stops listening, removes the socket from the checked directory and releases the lock.
func (l *SecureListener) Close() error {
	err := l.l.Close()
	l.once.Do(func() {
		_ = os.Remove(l.path)
		_ = windows.CloseHandle(l.lock)
		l.ch.close()
	})
	return err
}

// DialPath connects to a socket path.
func DialPath(path string) (*net.UnixConn, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return nil, errors.New("procbind: no socket path")
	}
	return DialUnix(path)
}

// DialSecure opens the socket directory the way ListenSecure made it (no reparse points, ours, no one else on the
// access list), checks the socket, and connects while the directories are held. It creates nothing. The checks do not
// authenticate the service: that is the keyed answer; the process that answered must also run as this user.
func DialSecure(o SocketOptions) (*net.UnixConn, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	ch, err := openChain(o, false)
	if err != nil {
		return nil, err
	}
	defer ch.close()
	path, err := sockPath(ch.dir)
	if err != nil {
		return nil, err
	}
	if err := checkSocketFile(path); err != nil {
		return nil, err
	}
	c, err := DialPath(path)
	if err != nil {
		return nil, err
	}
	if err := checkServerUser(c); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// checkServerUser requires the process that accepted to run as this user.
func checkServerUser(c *net.UnixConn) error {
	pi, err := peerProcess(c)
	if err != nil {
		return fmt.Errorf("%w: the service cannot be identified: %v", errUnsafeDir, err)
	}
	defer pi.proc.Close()
	me, err := OwnSID()
	if err != nil || pi.sid != me {
		return fmt.Errorf("%w: the service socket is served by another user", errUnsafeDir)
	}
	return nil
}

// probePeerPID connects a socket to itself and asks for the peer: it must be this process.
func probePeerPID() error {
	dir, err := os.MkdirTemp("", "mbp")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "p.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer l.Close()
	type res struct {
		c   *net.UnixConn
		err error
	}
	ch := make(chan res, 1)
	go func() { c, err := l.AcceptUnix(); ch <- res{c, err} }()
	cl, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return err
	}
	defer cl.Close()
	r := <-ch
	if r.err != nil {
		return r.err
	}
	defer r.c.Close()
	pid, err := peerPID(r.c)
	if err != nil {
		return err
	}
	if int(pid) != os.Getpid() {
		return errors.New("the peer pid is not this process")
	}
	return nil
}
