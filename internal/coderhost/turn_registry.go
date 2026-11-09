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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode"

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
var ErrRegistryUnsafe = errors.New("turn registry directory is not private to this user (on Windows a directory first created by an elevated host belongs to Administrators: delete it and start the host again)")

// ErrRegistryUnsupported is an ErrRegistryUnavailable for a platform that does not read process ancestry at
// all: nothing is written or read there, which is the design, not a fault worth printing at every start.
var ErrRegistryUnsupported = fmt.Errorf("%w: process ancestry cannot be read on this platform", ErrRegistryUnavailable)

// registeredCheckTimeout bounds the whole check of one registered database (its stat, open and queries), so a
// database on a hung mount cannot wedge every gated verb. Past it the check is an error like any unreadable one.
var registeredCheckTimeout = 3 * time.Second

// busyTimeout is how long a check of one database waits for a lock held on it. It is a variable only for the
// seam below; nothing in production assigns it.
var busyTimeout = 5 * time.Second

// SetBusyTimeoutForTest sets the wait for a locked database for the rest of the test. It panics outside a test
// binary and must not be combined with t.Parallel (the variable is shared).
func SetBusyTimeoutForTest(t testing.TB, d time.Duration) {
	t.Helper()
	if !testing.Testing() {
		panic("coderhost.SetBusyTimeoutForTest is a test seam and may only be used from a test binary")
	}
	t.Setenv("MONOAGENT_BUSY_TIMEOUT_TEST", "1") // makes t.Parallel panic
	prev := busyTimeout
	busyTimeout = d
	t.Cleanup(func() { busyTimeout = prev })
}

// scanBudget bounds the whole wait for locks in one scan of the registry (all entries together): a pile of busy
// live hosts must not make a recovery verb wait entries x the lock wait. Past it the entries not yet answered
// are reported busy (the verb refuses with the retry text: fail closed). A variable for the seam below.
var scanBudget = 8 * time.Second

// SetScanBudgetForTest sets scanBudget for the rest of the test (no t.Parallel; test binary only).
func SetScanBudgetForTest(t testing.TB, d time.Duration) {
	t.Helper()
	if !testing.Testing() {
		panic("coderhost.SetScanBudgetForTest is a test seam and may only be used from a test binary")
	}
	t.Setenv("MONOAGENT_SCAN_BUDGET_TEST", "1") // makes t.Parallel panic
	prev := scanBudget
	scanBudget = d
	t.Cleanup(func() { scanBudget = prev })
}

// ErrDatabaseBusy: a database is locked by another process past the wait for it (SQLite BUSY or LOCKED). Test
// it with errors.Is. It says nothing else about the database, which may be fine.
var ErrDatabaseBusy = errors.New("database busy (locked)")

// BusyHostError is ErrDatabaseBusy as the operator reads it: the live coder host that owns DB holds it locked.
// File is the registry entry that names the host, so an operator whose host is not really running can delete
// it; Others are the entry files of further busy live hosts (JoinBusy). The text carries nothing of the
// database or of the driver's error. The registry is writable by the same OS user, so every path is untrusted
// text: Error and JSONErrorFields strip control characters from them.
type BusyHostError struct {
	DB, File string
	Others   []string
}

// busyListMax is how many entry files a busy message names before it says "and N more".
const busyListMax = 5

func (e *BusyHostError) Error() string {
	if len(e.Others) == 0 {
		return "the coder host that owns " + printable(e.DB) + " is busy (database locked); wait and retry the command; the registry entry for it is " +
			shellQuote(printable(e.File)) + ": if that host is not running, delete this file and run the command again"
	}
	files := append([]string{e.File}, e.Others...)
	more := ""
	if len(files) > busyListMax {
		more = " and " + strconv.Itoa(len(files)-busyListMax) + " more"
		files = files[:busyListMax]
	}
	for i := range files {
		files[i] = shellQuote(printable(files[i]))
	}
	return strconv.Itoa(1+len(e.Others)) + " coder hosts are busy (database locked); wait and retry the command; the registry entries for them are " +
		strings.Join(files, ", ") + more + ": if those hosts are not running, delete these files and run the command again"
}

func (e *BusyHostError) Is(target error) bool { return target == ErrDatabaseBusy }

// JSONErrorFields is the --json error document of the refusal: the code host_busy (not operator_only), and
// the entry files (sanitized, at most busyListMax).
func (e *BusyHostError) JSONErrorFields() map[string]any {
	files := []string{printable(e.File)}
	for _, o := range e.Others {
		if len(files) < busyListMax {
			files = append(files, printable(o))
		}
	}
	return map[string]any{"code": "host_busy", "entries": files, "busy_count": 1 + len(e.Others)}
}

// JoinBusy folds the *BusyHostError values in errs (one per busy live host) into one that names all of them.
// nil when there are none.
func JoinBusy(errs ...error) error {
	var out *BusyHostError
	for _, err := range errs {
		var b *BusyHostError
		if !errors.As(err, &b) {
			continue
		}
		if out == nil {
			c := *b
			c.Others = append([]string(nil), b.Others...)
			out = &c
			continue
		}
		out.Others = append(out.Others, b.File)
		out.Others = append(out.Others, b.Others...)
	}
	if out == nil {
		return nil
	}
	return out
}

// shellQuote quotes s for a shell when it holds anything but plain path characters (single quotes, with a
// quote inside written as '\”), so the operator can paste the entry file into `rm`.
func shellQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-+=:,./@%", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// printable drops control, line-separator and format characters (newlines, escapes, bidi overrides) from s.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, s)
}

// ErrNoAnswer: a check of a registered database did not answer within its time bound.
var ErrNoAnswer = errors.New("no answer")

// damagedCodes are the SQLite primary result codes that say the DATABASE is unusable, not that its host did
// not answer: SQLITE_ERROR (1, e.g. a table or column missing), PERM (3), READONLY (8), IOERR (10), CORRUPT
// (11), CANTOPEN (14), AUTH (23) and NOTADB (26). A recovery verb lets the operator through such a database,
// since repairing the install is what the damage needs.
var damagedCodes = map[int]bool{1: true, 3: true, 8: true, 10: true, 11: true, 14: true, 23: true, 26: true}

// transientExtendedCodes are EXTENDED result codes whose primary code is on the damage list but that say the
// file could not be locked or recovered right now, not that it is damaged: IOERR_LOCK, IOERR_BLOCKED,
// IOERR_RDLOCK, IOERR_SHMLOCK, IOERR_CHECKRESERVEDLOCK, IOERR_SHMOPEN, IOERR_SHMSIZE, IOERR_SHMMAP, IOERR_NOMEM, READONLY_RECOVERY, READONLY_CANTINIT and
// READONLY_CANTLOCK. They are matched first, on the whole code; a plain IOERR, READONLY or CANTOPEN stays
// damage (CANTOPEN under file-descriptor pressure has no extended code of its own, so it cannot be told apart
// and is treated as damage).
var transientExtendedCodes = map[int]bool{
	10 | 15<<8: true, // SQLITE_IOERR_LOCK 3850
	10 | 11<<8: true, // SQLITE_IOERR_BLOCKED 2826
	10 | 9<<8:  true, // SQLITE_IOERR_RDLOCK 2314
	10 | 20<<8: true, // SQLITE_IOERR_SHMLOCK 5130
	10 | 14<<8: true, // SQLITE_IOERR_CHECKRESERVEDLOCK 3594
	10 | 18<<8: true, // SQLITE_IOERR_SHMOPEN 4618
	10 | 19<<8: true, // SQLITE_IOERR_SHMSIZE 4874
	10 | 21<<8: true, // SQLITE_IOERR_SHMMAP 5386
	10 | 12<<8: true, // SQLITE_IOERR_NOMEM 3082
	8 | 1<<8:   true, // SQLITE_READONLY_RECOVERY 264
	8 | 5<<8:   true, // SQLITE_READONLY_CANTINIT 1288
	8 | 2<<8:   true, // SQLITE_READONLY_CANTLOCK 520
}

// IsNonAnswer says whether err from checking the database of a coder host means "the host did not answer"
// rather than "the database is damaged": a lock past the wait (ErrDatabaseBusy), no answer in time
// (ErrNoAnswer), or any SQLite result code that is not in damagedCodes (SQLITE_PROTOCOL 15, NOMEM 7,
// INTERRUPT 9, ... and codes not known yet, which fail closed). Errors without a SQLite code (the file is
// not a regular file, it cannot be stat'ed, permission) are damage. For a LIVE host a non-answer refuses a
// recovery verb (a turn could be behind it); for a dead host, and for damage, the entry is skipped.
func IsNonAnswer(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDatabaseBusy) || errors.Is(err, ErrNoAnswer) {
		return true
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		if transientExtendedCodes[coded.Code()] {
			return true
		}
		return !damagedCodes[coded.Code()&0xff]
	}
	return false
}

// isBusy says whether err is SQLite's BUSY or LOCKED (primary result codes 5 and 6), however it is wrapped.
func isBusy(err error) bool {
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		c := coded.Code() & 0xff
		return c == 5 || c == 6
	}
	return false
}

// registeredDBCheck is the check of one registered database, waiting at most wait for a lock (a variable so a
// test can stand in for a hung one).
var registeredDBCheck = func(path string, wait time.Duration) (bool, error) {
	return callerInATurnAtWait(path, wait)
}

// lockWait is how long one registered database's check waits for a lock: the busy timeout, but at most two
// thirds of registeredCheckTimeout, so a locked database answers ErrDatabaseBusy before checkWithTimeout gives
// up on it.
func lockWait() time.Duration {
	wait := busyTimeout
	if limit := registeredCheckTimeout * 2 / 3; wait > limit {
		wait = limit
	}
	return wait
}

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
// writable by group or others (on Windows: whose owner or DACL lets anyone else write, see privateACL).
func checkRegistryDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 || !privateToUs(dir, fi, 0o022) {
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
	if err := makeRegistryDir(dir); err != nil {
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
	if err != nil || !fi.Mode().IsRegular() || !privateToUs(file, fi, 0o077) || fi.Size() > registryEntryMax {
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
// "not in a turn"; any other failure is an error (the caller refuses). A lock held past the busy timeout is
// ErrDatabaseBusy.
func CallerInATurnAt(path string) (bool, error) { return callerInATurnAtWait(path, busyTimeout) }

// CallerInATurnAtRecovery is CallerInATurnAt for the commands that repair an install: a lock is waited for
// only when a running coder host owns the database (a lock on any other database is not worth the wait: it
// would not refuse the verb anyway), so a dead host's locked database costs nothing.
func CallerInATurnAtRecovery(path string) (bool, error) {
	if _, ok := LiveHostEntryOf(path); !ok {
		return callerInATurnAtWait(path, 0)
	}
	return CallerInATurnAt(path)
}

func callerInATurnAtWait(path string, wait time.Duration) (bool, error) {
	in, err := callerInATurnAtRaw(path, wait)
	if err != nil && isBusy(err) {
		return false, fmt.Errorf("%w", ErrDatabaseBusy) // the driver's text stays out of every message
	}
	return in, err
}

func callerInATurnAtRaw(path string, wait time.Duration) (bool, error) {
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
	db, err := sql.Open("sqlite", "file:"+(&url.URL{Path: path}).EscapedPath()+"?mode=ro&_pragma=busy_timeout("+strconv.FormatInt(wait.Milliseconds(), 10)+")")
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

// checkWithTimeout runs registeredDBCheck on path (waiting at most wait for a lock) and gives up after timeout. A check stuck in
// a system call cannot be interrupted, so it is abandoned (the CLI exits soon after); the answer is an error.
func checkWithTimeout(path string, wait, timeout time.Duration) (bool, error) {
	type result struct {
		in  bool
		err error
	}
	ch := make(chan result, 1)
	check := registeredDBCheck // read here, not in the goroutine: an abandoned goroutine must not read the package var after a test restored it
	go func() {
		in, err := check(path, wait)
		ch <- result{in, err}
	}()
	select {
	case r := <-ch:
		return r.in, r.err
	case <-time.After(timeout):
		return false, fmt.Errorf("%w within %s", ErrNoAnswer, timeout)
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
	in, skipped, _ := scanRegisteredTurns(named, false)
	if in {
		return true, nil
	}
	if len(skipped) > 0 {
		return false, skipped[0]
	}
	return false, nil
}

// CallerInARegisteredTurnLenient is CallerInARegisteredTurn for the commands that repair an install: an
// entry whose database cannot be read is skipped (and returned in skipped, so a caller can say so) instead
// of ending the scan, so a turn recorded in ANY readable database is still found, wherever the unreadable
// entry sorts. in is true as soon as one readable database holds the caller as a turn.
//
// An entry that is locked past the wait (ErrDatabaseBusy) is not an unreadable one when its host is alive: it
// is returned in busy as a *BusyHostError, because a live host's database is exactly where a turn would be
// recorded and a lock held on it could hide that turn. A locked database of a dead host is skipped as before.
func CallerInARegisteredTurnLenient(named string) (in bool, skipped, busy []error) {
	return scanRegisteredTurns(named, true)
}

// LiveHostEntryOf returns the registry entry file of the registered, still running coder host whose database
// is the file at path ("", false when there is none).
func LiveHostEntryOf(path string) (file string, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	ents, err := registeredEntries()
	if err != nil {
		return "", false
	}
	for _, e := range ents {
		if efi, err := os.Stat(e.DB); err == nil && os.SameFile(fi, efi) && !hostIsGone(e) {
			return e.file, true
		}
	}
	return "", false
}

// scanRegisteredTurns is the one per-entry check behind both variants. Strict (lenient false) stops at the
// first unreadable live-host entry and returns its error; lenient records it and goes on.
func scanRegisteredTurns(named string, lenient bool) (bool, []error, []error) {
	if !ancestrySupported {
		return false, nil, nil
	}
	ents, err := registeredEntries()
	if err != nil {
		return false, nil, nil
	}
	var namedInfo os.FileInfo
	if named != "" {
		namedInfo, _ = os.Stat(named)
	}
	var todo []registryEntry
	for _, e := range ents {
		if namedInfo != nil {
			if fi, err := os.Stat(e.DB); err == nil && os.SameFile(namedInfo, fi) {
				continue
			}
		}
		todo = append(todo, e)
	}
	if !lenient {
		return scanStrict(todo)
	}
	return scanLenient(todo)
}

func unreadableEntryError(e registryEntry, err error) error {
	return fmt.Errorf("turn registry: the database %s of a coder host cannot be read (%v); the registry entry for it is %s; if that host is no longer running, delete this file and run the command again", e.DB, err, e.file)
}

// scanStrict stops at the first unreadable live-host entry. A dead host's database is never waited for (its
// lock wait is 0): its busy answer is skipped like any unreadable one.
func scanStrict(ents []registryEntry) (bool, []error, []error) {
	var skipped []error
	for _, e := range ents {
		gone := hostIsGone(e)
		wait := lockWait()
		if gone {
			wait = 0
		}
		in, err := checkWithTimeout(e.DB, wait, registeredCheckTimeout)
		if err != nil {
			if gone {
				continue
			}
			return false, append(skipped, unreadableEntryError(e, err)), nil
		}
		if in {
			return true, skipped, nil
		}
	}
	return false, skipped, nil
}

// scanLenient is the scan of the commands that repair an install, in two passes under one overall budget
// (scanBudget). Pass 1 asks every entry WITHOUT waiting for a lock, so a turn recorded in any readable
// database is found at once and a dead host's locked database costs nothing (it is skipped). Pass 2 waits for
// the locks of the live hosts that were busy, one after the other, until the budget is spent; the entries
// still locked then, and those not reached, are reported busy (fail closed: the verb says "wait and retry").
func scanLenient(ents []registryEntry) (bool, []error, []error) {
	var skipped, busy []error
	var pending []registryEntry
	deadline := time.Now().Add(scanBudget)
	// classify files an error of a live host's check: a non-answer is busy, damage is skipped.
	classify := func(e registryEntry, err error) {
		if IsNonAnswer(err) {
			busy = append(busy, &BusyHostError{DB: e.DB, File: e.file})
		} else {
			skipped = append(skipped, unreadableEntryError(e, err))
		}
	}
	for _, e := range ents {
		if hostIsGone(e) {
			// a dead host's database is judged when it answers at once and skipped otherwise: never waited for
			if in, err := checkWithTimeout(e.DB, 0, registeredCheckTimeout); err == nil && in {
				return true, skipped, nil
			}
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			busy = append(busy, &BusyHostError{DB: e.DB, File: e.file}) // not reached: fail closed
			continue
		}
		timeout := registeredCheckTimeout
		if limit := remaining + 500*time.Millisecond; timeout > limit {
			timeout = limit
		}
		in, err := checkWithTimeout(e.DB, 0, timeout)
		switch {
		case err == nil && in:
			return true, skipped, nil
		case err == nil:
		case errors.Is(err, ErrNoAnswer):
			classify(e, err) // a hung check is not retried
		case IsNonAnswer(err):
			pending = append(pending, e) // locked or otherwise silent: ask again, waiting for the lock
		default:
			classify(e, err)
		}
	}
	for _, e := range pending {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			busy = append(busy, &BusyHostError{DB: e.DB, File: e.file})
			continue
		}
		wait := lockWait()
		if wait > remaining {
			wait = remaining
		}
		timeout := registeredCheckTimeout
		if limit := remaining + 500*time.Millisecond; timeout > limit {
			timeout = limit
		}
		in, err := checkWithTimeout(e.DB, wait, timeout)
		switch {
		case err == nil && in:
			return true, skipped, nil
		case err == nil:
		default:
			classify(e, err)
		}
	}
	return false, skipped, busy
}
