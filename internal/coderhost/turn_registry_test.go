package coderhost

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/testdb"
)

func registryDirOf(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "coder-hosts")
	SetRegistryDirForTest(t, dir)
	return dir
}

func needAncestry(t *testing.T) {
	t.Helper()
	if !ancestrySupported || ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
}

func entryFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// (5) The seam: a test binary that did not set a directory has no registry, so no test reads or writes the real one.
func TestTheDefaultRegistryIsUnavailableInATestBinary(t *testing.T) {
	if why := RegistryStatus(); !strings.Contains(why, "unavailable") {
		t.Fatalf("a test binary without the seam has a registry: %q", why)
	}
	if _, err := RegisterHostDB(filepath.Join(t.TempDir(), "x.db")); err == nil {
		t.Fatal("registered into the default directory from a test binary")
	}
	if dbs, err := RegisteredHostDBs(); err == nil || len(dbs) != 0 {
		t.Fatalf("read the default registry from a test binary: %v %v", dbs, err)
	}
	if home, _ := account.SystemHome(); home != "" {
		if _, err := os.Stat(filepath.Join(home, ".monoagent", "coder-hosts")); err == nil {
			t.Log("note: the machine has a real registry; this binary did not touch it")
		}
	}
}

func TestRegistrationWritesOneEntryWithPrivateModes(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	db := filepath.Join(t.TempDir(), "a.db")
	un, err := RegisterHostDB(db)
	if err != nil {
		t.Fatal(err)
	}
	defer un()
	names := entryFiles(t, dir)
	if len(names) != 1 || !registryName.MatchString(names[0]) {
		t.Fatalf("entries: %v", names)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Errorf("directory mode %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Join(dir, names[0])); fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %v", fi.Mode().Perm())
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, names[0]))
	if !strings.Contains(string(raw), filepath.Base(db)) || strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Errorf("entry content: %s", raw)
	}
	dbs, err := RegisteredHostDBs()
	if err != nil || len(dbs) != 1 {
		t.Fatalf("listed %v %v", dbs, err)
	}
}

func TestTheSameDatabaseTwiceIsOneEntryAndTwoHostsAreTwo(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	a, b := filepath.Join(t.TempDir(), "a.db"), filepath.Join(t.TempDir(), "b.db")
	for _, p := range []string{a, a, b} {
		un, err := RegisterHostDB(p)
		if err != nil {
			t.Fatal(err)
		}
		defer un()
	}
	if n := len(entryFiles(t, dir)); n != 2 {
		t.Fatalf("%d entries, want 2", n)
	}
	if dbs, _ := RegisteredHostDBs(); len(dbs) != 2 {
		t.Fatalf("listed %v", dbs)
	}
}

func TestUnregisterRemovesOnlyItsOwnEntry(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	a, b := filepath.Join(t.TempDir(), "a.db"), filepath.Join(t.TempDir(), "b.db")
	unA, err := RegisterHostDB(a)
	if err != nil {
		t.Fatal(err)
	}
	unB, err := RegisterHostDB(b)
	if err != nil {
		t.Fatal(err)
	}
	defer unB()
	unA()
	if dbs, _ := RegisteredHostDBs(); len(dbs) != 1 || !strings.HasSuffix(dbs[0], "b.db") {
		t.Fatalf("after unregistering a: %v", dbs)
	}
	// an entry of another host (another pid) for a's path is not removed by this host's unregister
	foreign := filepath.Join(dir, entryName(canonicalOrSelf(t, a)))
	writeEntry(t, foreign, jsonEntry(canonicalOrSelf(t, a), 1, "x"), 0o600)
	unA()
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("unregister removed an entry that is not its own: %v", err)
	}
}

func canonicalOrSelf(t *testing.T, p string) string {
	t.Helper()
	c, err := canonicalDB(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// jsonEntry is an entry's content with the path escaped (a Windows path has backslashes).
func jsonEntry(db string, pid int, stamp string) string {
	raw, _ := json.Marshal(registryEntry{DB: db, PID: pid, Stamp: stamp})
	return string(raw)
}

func writeEntry(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// (3) hygiene: the reader skips what is not a well-formed entry, never deletes, and a stale entry is only listed.
func TestTheReaderSkipsBadEntriesAndDeletesNothing(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	good := filepath.Join(t.TempDir(), "good.db")
	un, err := RegisterHostDB(good)
	if err != nil {
		t.Fatal(err)
	}
	defer un()
	db := func(n string) string { return filepath.Join(t.TempDir(), n) }
	name := func(p string) string { return filepath.Join(dir, entryName(p)) }

	stale := db("stale.db") // its host is long gone: harmless, listed, not deleted
	writeEntry(t, name(stale), jsonEntry(stale, 999999, "gone"), 0o600)
	garbage := db("garbage.db")
	writeEntry(t, name(garbage), `not json`, 0o600)
	big := db("big.db")
	writeEntry(t, name(big), jsonEntry(big, 1, strings.Repeat("x", registryEntryMax)), 0o600)
	loose := db("loose.db")
	writeEntry(t, name(loose), jsonEntry(loose, 1, "x"), 0o666)
	wrongName := db("wrongname.db")
	writeEntry(t, filepath.Join(dir, strings.Repeat("a", 64)), jsonEntry(wrongName, 1, "x"), 0o600)
	relative := "relative.db"
	writeEntry(t, name(relative), `{"db":"relative.db","pid":1,"stamp":"x"}`, 0o600)
	writeEntry(t, filepath.Join(dir, "README"), `x`, 0o600)
	if runtime.GOOS != "windows" {
		target := db("linktarget.db")
		if err := os.Symlink(filepath.Join(dir, entryName(good)), name(target)); err != nil {
			t.Fatal(err)
		}
	}
	before := entryFiles(t, dir)

	dbs, err := RegisteredHostDBs()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(dbs, "\n")
	for _, want := range []string{canonicalOrSelf(t, good), stale} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing from %v", want, dbs)
		}
	}
	for _, bad := range []string{"garbage.db", "big.db", "loose.db", "wrongname.db", "relative.db", "linktarget.db"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s was listed: %v", bad, dbs)
		}
	}
	if after := entryFiles(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("the reader changed the directory:\n%v\n%v", before, after)
	}
}

func TestAUnsafeRegistryDirectoryIsRefused(t *testing.T) {
	needAncestry(t)
	if runtime.GOOS == "windows" {
		t.Skip("modes and symlinks")
	}
	t.Run("world-writable", func(t *testing.T) {
		dir := registryDirOf(t)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db")); err == nil {
			t.Error("registered into a world-writable directory")
		}
		if _, err := RegisteredHostDBs(); err == nil {
			t.Error("read a world-writable directory")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "coder-hosts")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		SetRegistryDirForTest(t, link)
		if _, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db")); err == nil {
			t.Error("registered through a symlinked directory")
		}
		if _, err := RegisteredHostDBs(); err == nil {
			t.Error("read through a symlinked directory")
		}
		if n := len(entryFiles(t, real)); n != 0 {
			t.Errorf("wrote %d files through the symlink", n)
		}
	})
}

func TestAMissingRegistryDirectoryIsEmptyNotAnError(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	if dbs, err := RegisteredHostDBs(); err != nil || len(dbs) != 0 {
		t.Fatalf("%v %v", dbs, err)
	}
}

// (4) a user with no passwd home: the registry is unavailable, which is today's behaviour.
func TestNoPasswdHomeMeansNoRegistry(t *testing.T) {
	needAncestry(t)
	SetRegistryDirForTest(t, "")
	if RegistryStatus() == "" {
		t.Fatal("status reports a registry")
	}
	if _, err := RegisterHostDB(filepath.Join(t.TempDir(), "a.db")); err == nil {
		t.Fatal("registered with no directory")
	}
	if in, err := CallerInARegisteredTurn(""); in || err != nil {
		t.Fatalf("no registry must add nothing: %v %v", in, err)
	}
}

func markTurn(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewStore(db).EnsureSession("default", "conv"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE coder_sessions SET turn_pid = ?, turn_stamp = ?`, os.Getpid(), ProcStamp(os.Getpid())); err != nil {
		t.Fatal(err)
	}
}

func TestCallerInARegisteredTurnSeesATurnAndChecksTheNamedFileOnce(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	x := testdb.Path(t)
	markTurn(t, x)
	un, err := RegisterHostDB(x)
	if err != nil {
		t.Fatal(err)
	}
	defer un()
	if in, err := CallerInARegisteredTurn(""); !in || err != nil {
		t.Fatalf("a registered turn was not seen: %v %v", in, err)
	}
	if in, err := CallerInARegisteredTurn(x); in || err != nil {
		t.Fatalf("the named file must be left to the caller: %v %v", in, err)
	}
	if in, err := CallerInATurnAt(x); !in || err != nil {
		t.Fatalf("CallerInATurnAt: %v %v", in, err)
	}
	// a read-only check never creates or migrates a database
	missing := filepath.Join(t.TempDir(), "missing.db")
	if in, err := CallerInATurnAt(missing); in || err != nil {
		t.Fatalf("%v %v", in, err)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("created the database")
	}
	bare := filepath.Join(t.TempDir(), "bare.db")
	b, _ := sql.Open("sqlite", "file:"+bare)
	b.Exec(`CREATE TABLE t(x)`)
	b.Close()
	if in, err := CallerInATurnAt(bare); in || err != nil {
		t.Fatalf("no coder_sessions table: %v %v", in, err)
	}
	if _, err := os.Stat(bare + ".migrate.lock"); err == nil {
		t.Fatal("migrated")
	}
}

func TestARegisteredDatabaseThatCannotBeQueriedIsAnError(t *testing.T) {
	needAncestry(t)
	registryDirOf(t)
	x := testdb.Path(t)
	d, _ := sql.Open("sqlite", "file:"+x)
	if _, err := d.Exec(`ALTER TABLE coder_sessions RENAME COLUMN turn_pid TO turn_pid_gone`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	un, err := RegisterHostDB(x)
	if err != nil {
		t.Fatal(err)
	}
	defer un()
	if in, err := CallerInARegisteredTurn(""); err == nil || in {
		t.Fatalf("an unreadable registered database must be an error: %v %v", in, err)
	}
}

// registerRaw writes an entry for db as the registry would (pid and stamp as given), creating the directory.
func registerRaw(t *testing.T, dir, db string, pid int, stamp string) string {
	t.Helper()
	if err := makeRegistryDir(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(registryEntry{DB: db, PID: pid, Stamp: stamp})
	file := filepath.Join(dir, entryName(db))
	writeEntry(t, file, string(raw), 0o600)
	return file
}

func gateWithin(t *testing.T, d time.Duration) (bool, error) {
	t.Helper()
	type res struct {
		in  bool
		err error
	}
	ch := make(chan res, 1)
	go func() { in, err := CallerInARegisteredTurn(""); ch <- res{in, err} }()
	select {
	case r := <-ch:
		return r.in, r.err
	case <-time.After(d):
		t.Fatalf("the gate did not answer within %s (a registered database wedged it)", d)
		return false, nil
	}
}

func TestAFifoRegisteredAsADatabaseDoesNotWedgeTheGate(t *testing.T) {
	needAncestry(t)
	fifoSupported(t)
	dir := registryDirOf(t)
	fifo := filepath.Join(t.TempDir(), "host.db")
	makeFifo(t, fifo)
	file := registerRaw(t, dir, canonicalOrSelf(t, fifo), os.Getpid(), ProcStamp(os.Getpid()))
	// the read itself refuses a non-regular file at once, it does not rely on the timeout
	// A watchdog makes a missing regular-file check fail in 5 s instead of hanging to the test timeout. The
	// goroutine stuck opening the FIFO may leak until the test process ends; that is acceptable in a test.
	start := time.Now()
	done := make(chan error, 1)
	go func() { _, err := CallerInATurnAt(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil || time.Since(start) > 2*time.Second {
			t.Fatalf("a FIFO must be refused at once: %v after %s", err, time.Since(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate hung on a non-regular registered file")
	}
	in, err := gateWithin(t, 6*time.Second)
	if in || err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "delete this file") {
		t.Fatalf("a live host's unreadable database must refuse and name the remedy: %v %v", in, err)
	}
}

func TestADirectoryRegisteredAsADatabaseRefusesWithTheRemedy(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	d := filepath.Join(t.TempDir(), "dir.db")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	file := registerRaw(t, dir, canonicalOrSelf(t, d), os.Getpid(), ProcStamp(os.Getpid()))
	in, err := gateWithin(t, 6*time.Second)
	if in || err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "delete this file") {
		t.Fatalf("%v %v", in, err)
	}
}

func TestAHungDatabaseCheckTimesOutAndFailsClosedWithTheRemedy(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	x := testdb.Path(t)
	file := registerRaw(t, dir, canonicalOrSelf(t, x), os.Getpid(), ProcStamp(os.Getpid()))
	block := make(chan struct{})
	defer close(block)
	prevCheck, prevTimeout := registeredDBCheck, registeredCheckTimeout
	registeredDBCheck = func(string, time.Duration) (bool, error) { <-block; return false, nil }
	registeredCheckTimeout = 200 * time.Millisecond
	defer func() { registeredDBCheck, registeredCheckTimeout = prevCheck, prevTimeout }()
	in, err := gateWithin(t, 5*time.Second)
	if in || err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("%v %v", in, err)
	}
}

// A registered database that cannot be read refuses while its host lives and is skipped once the host is gone.
func TestAnUnreadableDatabaseOfADeadHostIsSkippedAndOfALiveHostRefuses(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	junk := filepath.Join(t.TempDir(), "junk.db")
	writeEntry(t, junk, strings.Repeat("not a database ", 500), 0o600)
	cj := canonicalOrSelf(t, junk)
	file := registerRaw(t, dir, cj, os.Getpid(), "bogus-stamp") // the pid lives but is not the process the entry recorded
	if in, err := gateWithin(t, 6*time.Second); in || err != nil {
		t.Fatalf("a dead host's unreadable database must be skipped: %v %v", in, err)
	}
	registerRaw(t, dir, cj, os.Getpid(), ProcStamp(os.Getpid()))
	if in, err := gateWithin(t, 6*time.Second); in || err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("a live host's unreadable database must refuse: %v %v", in, err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("the reader must never delete an entry")
	}
	// a pid that is gone altogether counts as dead too
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot run a child")
	}
	registerRaw(t, dir, cj, cmd.Process.Pid, "whatever")
	if in, err := gateWithin(t, 6*time.Second); in || err != nil {
		t.Fatalf("an exited host: %v %v", in, err)
	}
	// an entry without a recorded stamp cannot be shown dead: it stays fail-closed
	registerRaw(t, dir, cj, cmd.Process.Pid, "")
	if _, err := gateWithin(t, 6*time.Second); err == nil {
		t.Fatal("an entry with no stamp must stay fail-closed")
	}
}

// A dead host's database that CAN be read is still judged: only an unreadable one is skipped.
func TestAReadableDatabaseOfADeadHostIsStillChecked(t *testing.T) {
	needAncestry(t)
	dir := registryDirOf(t)
	x := testdb.Path(t)
	markTurn(t, x)
	registerRaw(t, dir, canonicalOrSelf(t, x), os.Getpid(), "bogus-stamp")
	if in, err := gateWithin(t, 6*time.Second); !in || err != nil {
		t.Fatalf("%v %v", in, err)
	}
}

// busy_timeout(5000) on the read-only open is what makes a briefly locked database wait instead of failing at
// once, and a long lock fail after a bounded wait. No other test holds a lock, so removing the pragma (or an
// unbounded wait) is caught only here.
func TestALockedDatabaseIsWaitedOnForTheBusyTimeoutThenAnError(t *testing.T) {
	needAncestry(t)
	path := testdb.Path(t)
	markTurn(t, path)
	w, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.SetMaxOpenConns(1)
	// a rollback-journal database: in WAL mode readers are never blocked by a writer
	if _, err := w.Exec(`PRAGMA journal_mode = DELETE`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	in, err := CallerInATurnAt(path)
	waited := time.Since(start)
	if in || err == nil {
		t.Fatalf("a database locked for longer than the timeout: in=%v err=%v", in, err)
	}
	if waited < 4*time.Second || waited > 12*time.Second {
		t.Fatalf("waited %v on the lock, want about the 5 s busy timeout", waited)
	}
	if _, err := w.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if in, err := CallerInATurnAt(path); err != nil || !in {
		t.Fatalf("after the lock is released: in=%v err=%v", in, err)
	}
}
