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
	"syscall"
	"testing"
	"time"

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

// ErrRegistryUnsupported is an ErrRegistryUnavailable for a platform that does not read process ancestry at
// all: nothing is written or read there, which is the design, not a fault worth printing at every start.
var ErrRegistryUnsupported = fmt.Errorf("%w: process ancestry cannot be read on this platform", ErrRegistryUnavailable)

// registeredCheckTimeout bounds the whole check of one registered database (its stat, open and queries), so a
// database on a hung mount cannot wedge every gated verb. Past it the check is an error like any unreadable one.
var registeredCheckTimeout = 3 * time.Second

// registeredDBCheck is the check of one registered database (a variable so a test can stand in for a hung one).
var registeredDBCheck = CallerInATurnAt

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
		return "", ErrRegistryUnsupported
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
	file  string // the entry's own path (set by the reader; not part of the file)
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
// Limit: two hosts on one database share that one entry (the later start overwrites the pid); if the later one
// exits first the entry is gone while the earlier one still runs, and the gates then see only the database a
// command names for that host. Entries are one per running host database, and the gate opens each once.
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
	ents, err := registeredEntries()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.DB)
	}
	return out, nil
}

// registeredEntries is RegisteredHostDBs with the entries themselves, one per database, sorted by it.
func registeredEntries() ([]registryEntry, error) {
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
	var out []registryEntry
	for _, de := range ents {
		if !registryName.MatchString(de.Name()) {
			continue
		}
		file := filepath.Join(dir, de.Name())
		if e, ok := readRegistryEntry(file); ok && !seen[e.DB] {
			seen[e.DB] = true
			e.file = file
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DB < out[j].DB })
	return out, nil
}

// CallerInATurnAt says whether the calling process descends from a turn recorded in the database at path,
// opened read-only: it never creates, migrates or writes it. A missing file or no coder_sessions table is
// "not in a turn"; any other failure is an error (the caller refuses).
func CallerInATurnAt(path string) (bool, error) {
	if !ancestrySupported {
		return false, nil
	}
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// Only a regular file is opened: opening a FIFO or a device blocks without bound, and busy_timeout does
	// not cover the open.
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
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

// checkWithTimeout runs registeredDBCheck on path and gives up after registeredCheckTimeout. A check stuck in
// a system call cannot be interrupted, so it is abandoned (the CLI exits soon after); the answer is an error.
func checkWithTimeout(path string) (bool, error) {
	type result struct {
		in  bool
		err error
	}
	ch := make(chan result, 1)
	check := registeredDBCheck // read here, not in the goroutine: an abandoned goroutine must not read the package var after a test restored it
	go func() {
		in, err := check(path)
		ch <- result{in, err}
	}()
	select {
	case r := <-ch:
		return r.in, r.err
	case <-time.After(registeredCheckTimeout):
		return false, fmt.Errorf("no answer within %s", registeredCheckTimeout)
	}
}

// procStamp and signalPID are the two probes hostIsGone makes of a recorded pid. They are variables only so a
// test can replace them (no production code assigns them); the defaults are the calls hostIsGone always made.
var (
	procStamp = ProcStamp
	signalPID = func(pid int) error {
		p, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find process %d: %v", pid, err) // %v: a lookup failure is never "the process is done"
		}
		return p.Signal(syscall.Signal(0))
	}
)

// hostIsGone says whether the process that wrote an entry is certainly not running: its pid now carries a
// different start stamp (a reused pid) or no process at all. An entry without a recorded stamp, or a pid that
// cannot be judged, is NOT gone (the caller then fails closed).
func hostIsGone(e registryEntry) bool {
	if e.PID <= 0 || e.Stamp == "" {
		return false
	}
	if now := procStamp(e.PID); now != "" {
		return now != e.Stamp
	}
	return errors.Is(signalPID(e.PID), os.ErrProcessDone)
}

// CallerInARegisteredTurn is the registry half of the operator gates: whether the caller descends from a turn
// recorded in any registered host's database, except named (the database the process itself names, which the
// gate checks its own way; the same file is checked once). A registry that is unavailable, unsafe or whose
// directory cannot be listed adds nothing (the gate is then what it was before the registry; by decision).
//
// A registered database that cannot be read (or does not answer within registeredCheckTimeout) is an error, so
// the gate refuses, and the message names the entry file: if that host is gone the operator deletes it.
//
// One exception, for an entry whose recorded host is certainly dead (hostIsGone): its unreadable database is
// skipped, so a crashed host (typical of one run under a scratch HOME) cannot lock the operator out. Why that
// is sound: a dead host starts no new turn, so no turn it would still record can begin; what remains is a
// turn process that outlived it, and that one is found through the rows of this very database, which we
// cannot read. The residual: such an orphan, whose host died and whose database ALSO became unreadable, is not
// seen by this layer. Reaching it needs the host to die and the database to fail (or a same-uid process to do
// both), a process of the same OS user that could instead edit the database or delete the entry, which the
// courtesy layer already does not stop (SECURITY.md). A database that is readable is always judged, host dead
// or not, and a live host's unreadable database always refuses.
func CallerInARegisteredTurn(named string) (bool, error) {
	if !ancestrySupported {
		return false, nil
	}
	ents, err := registeredEntries()
	if err != nil {
		return false, nil
	}
	var namedInfo os.FileInfo
	if named != "" {
		namedInfo, _ = os.Stat(named)
	}
	for _, e := range ents {
		if namedInfo != nil {
			if fi, err := os.Stat(e.DB); err == nil && os.SameFile(namedInfo, fi) {
				continue
			}
		}
		in, err := checkWithTimeout(e.DB)
		if err != nil {
			if hostIsGone(e) {
				continue
			}
			return false, fmt.Errorf("turn registry: the database %s of a coder host cannot be read (%v); the registry entry for it is %s; if that host is no longer running, delete this file and run the command again", e.DB, err, e.file)
		}
		if in {
			return true, nil
		}
	}
	return false, nil
}
