package stop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// A unix socket address holds about 104 bytes on macOS and 108 elsewhere, and the run directory is
// <home>/.monoagent/doomsday/<profile id>, so a long home name pushes the natural socket path over. A path that does not
// fit moves to a short private directory of the user's, named from a hash of the run directory: the listener and the
// dialer both compute it from the RunDir, so they agree. The directory is made 0700 and is refused, never repaired, when it
// is a symlink, another user's, or open to others; the sockets in it keep their peer-uid check.

// dialPeerUID reads the peer of a connection we made (a seam for tests).
var dialPeerUID = peerUID

// sockMax is the limit in force; tests lower it.
var sockMax = platformSockMax

// shortSockBase is where the short directories live (tests replace it).
var shortSockBase = func() string {
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat("/tmp"); err == nil && fi.IsDir() {
			return "/tmp"
		}
	}
	return os.TempDir()
}

// sockPath is the path of the socket called name for this run directory.
func (r RunDir) sockPath(name string) string {
	natural := filepath.Join(r.Dir, name)
	if len(natural) <= sockMax {
		return natural
	}
	h := sha256.Sum256([]byte(filepath.Clean(r.Dir)))
	return filepath.Join(shortSockBase(), "monoagent-"+strconv.Itoa(os.Getuid())+"-"+hex.EncodeToString(h[:6]), name)
}

// prepareSock makes the directory of a socket path usable: the run directory itself is made by the caller; a short
// directory is made here and checked.
func (r RunDir) prepareSock(path string) error {
	dir := filepath.Dir(path)
	if dir == filepath.Clean(r.Dir) {
		return nil
	}
	return ensurePrivateDir(dir)
}

func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("the short socket directory %s cannot be made: %w", dir, err)
	}
	return checkPrivateDir(dir)
}

// checkPrivateDir requires dir to be a plain directory of this user, closed to others. A wrong one is refused, never repaired:
// someone else may have made it. (The directory is in /tmp, which is sticky, so no one else can replace one that is ours.)
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	remedy := fmt.Sprintf("; if you did not make it, ask an administrator to delete the directory %s, or stop the process that made it", dir)
	if !fi.IsDir() {
		return fmt.Errorf("the short socket directory %s is not a plain directory (a link or a file, owned by %s)%s", dir, ownerOf(fi), remedy)
	}
	if !fileOwnedByMe(fi) {
		return fmt.Errorf("the short socket directory %s belongs to %s, not to this user: refusing it%s", dir, ownerOf(fi), remedy)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("the short socket directory %s is open to others (mode %v): refusing it%s", dir, fi.Mode().Perm(), remedy)
	}
	return nil
}

// dialSock connects to a socket of this run. A socket in the short directory is trusted only after the directory (ours, 0700,
// not a link) and the socket file (a socket of ours) are checked and, once connected, the peer is this user: the short
// directory's name is predictable, so without this another local user could make it first and answer in the host's place.
func (r RunDir) dialSock(path string) (net.Conn, error) {
	short := filepath.Dir(path) != filepath.Clean(r.Dir)
	if short {
		if err := checkPrivateDir(filepath.Dir(path)); err != nil {
			return nil, err
		}
		fi, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSocket == 0 || !fileOwnedByMe(fi) {
			return nil, fmt.Errorf("%s is not a socket of this user (owned by %s): refusing it", path, ownerOf(fi))
		}
	}
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil || !short || runtime.GOOS == "windows" {
		return c, err
	}
	if uid, perr := dialPeerUID(c); perr != nil || int(uid) != os.Getuid() {
		c.Close()
		return nil, fmt.Errorf("%s is served by another user (%d, %v): refusing it", path, uid, perr)
	}
	return c, nil
}

// probeSockDir is the directory the start-time self-test binds in for the run directory run, and what removes it afterwards:
// run itself when the real sockets fit there. Otherwise the short directory they would use: checked when it exists (so a
// squatted one fails the probe) and never made here (a show or dry run must not create it); when it does not exist yet the
// probe binds in a private scratch directory next to it, which proves the place can hold a socket.
func probeSockDir(run string) (string, func(), error) {
	r := RunDir{Dir: run}
	dir, none := run, func() {}
	for _, name := range []string{"anchor.sock", "e18446744073709551615.sock"} {
		p := r.sockPath(name)
		d := filepath.Dir(p)
		if d == filepath.Clean(run) {
			continue
		}
		if _, err := os.Lstat(d); err == nil {
			if err := checkPrivateDir(d); err != nil {
				return "", none, err
			}
			dir = d
			continue
		}
		scratch, err := os.MkdirTemp(shortSockBase(), ".ma-peer-")
		if err != nil {
			return "", none, fmt.Errorf("a socket cannot be made in %s: %w", shortSockBase(), err)
		}
		return scratch, func() { _ = os.RemoveAll(scratch) }, nil
	}
	return dir, none, nil
}
