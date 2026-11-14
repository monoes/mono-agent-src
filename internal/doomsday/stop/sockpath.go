package stop

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// A unix socket address holds about 104 bytes on macOS and 108 elsewhere, and the run directory is
// <home>/.monoagent/doomsday/<profile id>, so a long home name pushes the natural socket path over. A path that does not
// fit moves to a short private directory of the user's, named from a hash of the run directory: the listener and the
// dialer both compute it from the RunDir, so they agree. The directory is made 0700 and is refused, never repaired, when it
// is a symlink, another user's, or open to others; the sockets in it keep their peer-uid check.

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
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("the short socket directory %s is not a plain directory (a link or a file): remove it", dir)
	}
	if !fileOwnedByMe(fi) {
		return fmt.Errorf("the short socket directory %s belongs to another user: refusing it", dir)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("the short socket directory %s is open to others (mode %v): refusing it", dir, fi.Mode().Perm())
	}
	return nil
}

// probeSockDir is the directory the start-time self-test binds in for the run directory run: run itself when the real
// sockets fit there, else the short directory they would use (made and checked, so a refusal shows at the probe).
func probeSockDir(run string) (string, error) {
	r := RunDir{Dir: run}
	dir := run
	for _, name := range []string{"anchor.sock", "e18446744073709551615.sock"} {
		p := r.sockPath(name)
		if filepath.Dir(p) != filepath.Clean(run) {
			if err := r.prepareSock(p); err != nil {
				return "", err
			}
			dir = filepath.Dir(p)
		}
	}
	return dir, nil
}
