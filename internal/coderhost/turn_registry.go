package coderhost

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	_ "modernc.org/sqlite" // the driver of the read-only opens below (already linked by internal/storage)

	"github.com/monoes/mono-agent/internal/account"
)

// The turn registry: every running coder host records the database it runs turns against in a file under the
// OS user's own home (<passwd home>/.monoagent/coder-hosts/, never $HOME), and the operator gates check the
// union of the database the calling process names and every registered one. A process that points the CLI
// at another database (--db-path, another HOME, an empty file) therefore still has the hosts' databases
// checked. An override can only ADD checks. A courtesy layer of the class SECURITY.md describes: a same-uid
// process that deletes the entry or edits the database is not stopped by it.

// ErrRegistryUnavailable: there is no registry here (no passwd home for this user, ancestry cannot be read on
// this platform, or a test binary that did not set a directory). The gates then behave as they did before it.
var ErrRegistryUnavailable = errors.New("turn registry unavailable")

// ErrRegistryUnsafe: the registry directory is a symlink, belongs to someone else or is writable by others.
var ErrRegistryUnsafe = errors.New("turn registry directory is not private to this user")

const (
	registryEntryMax = 4096
	registrySubdir   = ".monoagent/coder-hosts"
)

var registryName = regexp.MustCompile(`^[0-9a-f]{64}$`)

// registryDir is the test seam and the one place the directory is decided. The default refuses to run inside
// a test binary (testing.Testing, a property of the binary an environment variable cannot change), so no test
// can read or write the real registry; a test that wants one calls SetRegistryDirForTest.
var registryDir = defaultRegistryDir

func defaultRegistryDir() (string, error) {
	if !ancestrySupported {
		return "", fmt.Errorf("%w: process ancestry cannot be read on this platform", ErrRegistryUnavailable)
	}
	if testing.Testing() {
		return "", fmt.Errorf("%w: a test binary uses SetRegistryDirForTest", ErrRegistryUnavailable)
	}
	home, err := account.SystemHome()
	if err != nil || home == "" {
		return "", fmt.Errorf("%w: the system has no home directory for this OS user", ErrRegistryUnavailable)
	}
	return filepath.Join(home, filepath.FromSlash(registrySubdir)), nil
}

// SetRegistryDirForTest points the registry at dir for the rest of the test; "" makes it unavailable. It
// panics outside a test binary and, like the account seams, must not be combined with t.Parallel.
func SetRegistryDirForTest(t testing.TB, dir string) {
	t.Helper()
	if !testing.Testing() {
		panic("coderhost.SetRegistryDirForTest is a test seam and may only be used from a test binary")
	}
	t.Setenv("MONOAGENT_TURN_REGISTRY_TEST", "1") // makes t.Parallel panic, as the account seams do
	prev := registryDir
	registryDir = func() (string, error) {
		if dir == "" {
			return "", ErrRegistryUnavailable
		}
		return dir, nil
	}
	t.Cleanup(func() { registryDir = prev })
}

// RegistryStatus is "" when the registry works here, else why it does not (for host status and doctor).
func RegistryStatus() string {
	if _, err := registryDir(); err != nil {
		return err.Error()
	}
	return ""
}

type registryEntry struct {
	DB    string `json:"db"`
	PID   int    `json:"pid"`
	Stamp string `json:"stamp"`
}

func entryName(db string) string {
	sum := sha256.Sum256([]byte(db))
	return hex.EncodeToString(sum[:])
}

// canonicalDB is the absolute path of db with symlinks resolved as far as the file exists.
func canonicalDB(db string) (string, error) {
	abs, err := filepath.Abs(db)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, nil
	}
	return abs, nil
}

// checkRegistryDir refuses a directory that is a symlink, is not a directory, is not owned by this user or is
// writable by group or others.
func checkRegistryDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 || !ownedByUs(fi) || fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s", ErrRegistryUnsafe, dir)
	}
	return nil
}

// RegisterHostDB records that a coder host runs turns against the database at path, until unregister is
// called. The entry is one file named by the hash of the canonical path (so the same database twice is one
// entry), 0600, in a 0700 directory, written atomically. It holds the path, the host's pid and start stamp and
// nothing else. unregister removes the entry only while it is still this host's.
func RegisterHostDB(path string) (unregister func(), err error) {
	noop := func() {}
	dir, err := registryDir()
	if err != nil {
		return noop, err
	}
	db, err := canonicalDB(path)
	if err != nil {
		return noop, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return noop, err
	}
	if err := checkRegistryDir(dir); err != nil {
		return noop, err
	}
	pid := os.Getpid()
	raw, _ := json.Marshal(registryEntry{DB: db, PID: pid, Stamp: ProcStamp(pid)})
	file := filepath.Join(dir, entryName(db))
	tmp := fmt.Sprintf("%s.tmp-%d", file, pid)
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return noop, err
	}
	_, werr := f.Write(raw)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, file)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return noop, werr
	}
	return func() {
		if e, ok := readRegistryEntry(file); ok && e.PID == pid {
			_ = os.Remove(file)
		}
	}, nil
}

// readRegistryEntry reads one entry file; ok is false for anything that is not a well-formed entry of this user
// (a symlink, another owner, loose modes, oversized or garbage content, a name that is not its path's hash).
func readRegistryEntry(file string) (registryEntry, bool) {
	var e registryEntry
	fi, err := os.Lstat(file)
	if err != nil || !fi.Mode().IsRegular() || !ownedByUs(fi) || fi.Mode().Perm()&0o077 != 0 || fi.Size() > registryEntryMax {
		return e, false
	}
	f, err := os.Open(file)
	if err != nil {
		return e, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, registryEntryMax+1))
	if err != nil || len(raw) > registryEntryMax || json.Unmarshal(raw, &e) != nil {
		return e, false
	}
	if !filepath.IsAbs(e.DB) || filepath.Base(file) != entryName(e.DB) {
		return e, false
	}
	return e, true
}

// RegisteredHostDBs lists the databases the registered hosts name, sorted. It changes nothing: an entry whose
// host is gone is still listed (the turn rows' start stamps fail the ancestry check, so it costs one query),
// and an entry that cannot be read is left out. An unavailable registry is ErrRegistryUnavailable; a missing
// directory is an empty list; an unsafe directory is ErrRegistryUnsafe.
func RegisteredHostDBs() ([]string, error) {
	dir, err := registryDir()
	if err != nil {
		return nil, err
	}
	if err := checkRegistryDir(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, de := range ents {
		if !registryName.MatchString(de.Name()) {
			continue
		}
		if e, ok := readRegistryEntry(filepath.Join(dir, de.Name())); ok && !seen[e.DB] {
			seen[e.DB] = true
			out = append(out, e.DB)
		}
	}
	sort.Strings(out)
	return out, nil
}

// CallerInATurnAt says whether the calling process descends from a turn recorded in the database at path,
// opened read-only: it never creates, migrates or writes it. A missing file or no coder_sessions table is
// "not in a turn"; any other failure is an error (the caller refuses).
func CallerInATurnAt(path string) (bool, error) {
	if !ancestrySupported {
		return false, nil
	}
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return false, err
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'coder_sessions'`).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	return NewStore(db).CallerInATurn()
}

// CallerInARegisteredTurn is the registry half of the operator gates: whether the caller descends from a turn
// recorded in any registered host's database, except named (the database the process itself names, which the
// gate checks its own way; the same file is checked once). A registry that is unavailable or unsafe adds
// nothing (the gate is then what it was before the registry). A registered database that cannot be read is an
// error, so the gate refuses.
func CallerInARegisteredTurn(named string) (bool, error) {
	if !ancestrySupported {
		return false, nil
	}
	dbs, err := RegisteredHostDBs()
	if err != nil {
		return false, nil
	}
	var namedInfo os.FileInfo
	if named != "" {
		namedInfo, _ = os.Stat(named)
	}
	for _, p := range dbs {
		if namedInfo != nil {
			if fi, err := os.Stat(p); err == nil && os.SameFile(namedInfo, fi) {
				continue
			}
		}
		in, err := CallerInATurnAt(p)
		if err != nil {
			return false, fmt.Errorf("turn registry: %s: %w", p, err)
		}
		if in {
			return true, nil
		}
	}
	return false, nil
}
