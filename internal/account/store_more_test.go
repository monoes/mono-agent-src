package account_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// countingSealer counts the calls that reach the key store and can make it fail.
// It is not safe for concurrent use: no test shares one between goroutines.
type countingSealer struct {
	inner        account.Sealer
	err          error
	seals, opens int
}

func (c *countingSealer) Seal(p []byte) ([]byte, error) {
	c.seals++
	if c.err != nil {
		return nil, c.err
	}
	return c.inner.Seal(p)
}

func (c *countingSealer) Open(b []byte) ([]byte, error) {
	c.opens++
	if c.err != nil {
		return nil, c.err
	}
	return c.inner.Open(b)
}

// snapshot reads every file of dir, so a test can show that nothing in it changed.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range names(t, dir) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(data)
	}
	return out
}

// Nothing reaches the key store unless there is a token to seal or to open: on a
// real machine the sealer is the keychain, and a reach is a prompt.
func TestTheKeyStoreIsReachedOnlyWhenThereIsATokenToSealOrOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account")
	cs := &countingSealer{inner: account.NewMemorySealer()}
	st := account.OpenStore(dir, cs)
	if rt, err := st.LoadRefresh(); rt != "" || err != nil {
		t.Fatalf("LoadRefresh with no refresh.enc = a token %v, %v, want none and no error", rt != "", err)
	}
	if err := st.SaveRefresh(""); err == nil {
		t.Fatal("an empty refresh token must be refused")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused refresh token created %s (stat err %v)", dir, err)
	}
	if err := st.Save(&account.Session{Host: "h"}); err != nil {
		t.Fatal(err)
	}
	if rt, err := st.LoadRefresh(); rt != "" || err != nil {
		t.Fatalf("LoadRefresh beside a session but with no refresh.enc = a token %v, %v", rt != "", err)
	}
	if cs.seals != 0 || cs.opens != 0 {
		t.Fatalf("the key store was reached %d times to seal and %d to open with no token to handle", cs.seals, cs.opens)
	}
	if err := st.SaveRefresh("rt-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.LoadRefresh(); err != nil || got != "rt-1" || cs.seals != 1 || cs.opens != 1 {
		t.Fatalf("with a token the key store is reached once to seal and once to open: match=%v err=%v seals=%d opens=%d", got == "rt-1", err, cs.seals, cs.opens)
	}
}

// A refresh token that cannot be sealed is not written, and the refresh.enc
// already on disk is left alone (whether to delete it is the caller's call). The
// key store's own error reaches the caller, whose message ("sign in again") is
// for the user.
func TestASealerFailureLeavesTheOldRefreshTokenAndItsErrorReachesTheCaller(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account")
	good := account.OpenStore(dir, account.NewMemorySealer())
	if err := good.SaveRefresh("rt-1"); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)

	refused := errors.New("the key store said no")
	failing := account.OpenStore(dir, &countingSealer{err: refused})
	if err := failing.SaveRefresh("rt-2"); !errors.Is(err, refused) {
		t.Fatalf("SaveRefresh err = %v, want the key store's own error", err)
	}
	if _, err := failing.LoadRefresh(); !errors.Is(err, refused) {
		t.Fatalf("LoadRefresh err = %v, want the key store's own error", err)
	}
	if !reflect.DeepEqual(snapshot(t, dir), before) {
		t.Fatalf("a refresh token the key store refused changed the directory (now %v)", names(t, dir))
	}
	if got, err := good.LoadRefresh(); err != nil || got != "rt-1" {
		t.Fatalf("the old refresh token did not survive: match=%v err=%v", got == "rt-1", err)
	}
}

// With no home directory there is no session: the store must not fall back to
// the process's working directory, where a session.json or a refresh.enc of some
// other program or project may sit.
func TestWithoutAHomeTheWorkingDirectoryIsNeverReadWrittenOrDeleted(t *testing.T) {
	cwd := t.TempDir()
	decoy := account.OpenStore(cwd, account.NewMemorySealer())
	if err := decoy.Save(&account.Session{Host: "decoy"}); err != nil {
		t.Fatal(err)
	}
	if err := decoy.SaveRefresh("rt-decoy"); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, cwd)

	t.Chdir(cwd)
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	cs := &countingSealer{inner: account.NewMemorySealer()}
	st := account.OpenStore("", cs)

	if sess, err := st.Load(); sess != nil || err != nil {
		t.Errorf("Load = a session %v, %v, want none: ./session.json is not the user's session", sess != nil, err)
	}
	if rt, err := st.LoadRefresh(); rt != "" || err != nil {
		t.Errorf("LoadRefresh = a token %v, %v, want none", rt != "", err)
	}
	if mt, err := st.Mtime(); !mt.IsZero() || err != nil {
		t.Errorf("Mtime = %v, %v, want zero and no error", mt, err)
	}
	if err := st.DeleteRefresh(); err != nil {
		t.Errorf("DeleteRefresh: %v", err)
	}
	unlock, lockErr := st.Lock(context.Background())
	if lockErr == nil {
		unlock()
	}
	for _, c := range []struct {
		call string
		err  error
	}{
		{"Save", st.Save(&account.Session{Host: "h"})},
		{"SaveRefresh", st.SaveRefresh("rt-x")},
		{"Lock", lockErr},
	} {
		if c.err == nil || !strings.Contains(c.err.Error(), "home directory") {
			t.Errorf("%s: err = %v, want a refusal that names the missing home directory", c.call, c.err)
		}
	}
	if cs.seals != 0 || cs.opens != 0 {
		t.Errorf("the key store was reached %d times to seal and %d to open", cs.seals, cs.opens)
	}
	if !reflect.DeepEqual(snapshot(t, cwd), before) {
		t.Errorf("the working directory changed: it held two files, it holds %v", names(t, cwd))
	}
}

// A file or a directory that cannot be read is an error. "None" would make a
// permissions problem or a disk fault look like a machine that never signed in.
func TestAnIOErrorIsNeverAnsweredAsNone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a path through a file as not found, which is a different answer")
	}
	root := t.TempDir()

	// A regular file where the directory should be: every path below it fails
	// with "not a directory", which is not "does not exist".
	blocker := filepath.Join(root, "account")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := account.OpenStore(blocker, account.NewMemorySealer())
	sess, loadErr := st.Load()
	_, mtimeErr := st.Mtime()
	rt, refreshErr := st.LoadRefresh()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, lockErr := st.Lock(ctx)
	if lockErr == nil {
		unlock()
	}
	for _, c := range []struct {
		call string
		err  error
	}{
		{"Load", loadErr}, {"Mtime", mtimeErr}, {"LoadRefresh", refreshErr}, {"Lock", lockErr},
		{"DeleteRefresh", st.DeleteRefresh()},
		{"Save", st.Save(&account.Session{Host: "h"})},
		{"SaveRefresh", st.SaveRefresh("rt-1")},
	} {
		if c.err == nil {
			t.Errorf("%s with a regular file where the directory should be: no error", c.call)
		}
	}
	if sess != nil || rt != "" {
		t.Errorf("a session %v or a refresh token %v came out of a directory that is a file", sess != nil, rt != "")
	}

	// session.json and refresh.enc that are directories are refused too: they are
	// not regular files (readStoreFile).
	dir := filepath.Join(root, "real")
	for _, name := range []string{"session.json", "refresh.enc"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	st = account.OpenStore(dir, account.NewMemorySealer())
	if sess, err := st.Load(); sess != nil || err == nil {
		t.Errorf("Load with session.json a directory = a session %v, %v, want an error", sess != nil, err)
	}
	if rt, err := st.LoadRefresh(); rt != "" || err == nil {
		t.Errorf("LoadRefresh with refresh.enc a directory = a token %v, %v, want an error", rt != "", err)
	}
}

// Beyond bad JSON and a bad version: a field of the wrong type is a corrupt
// file too, and so is a time that does not parse, which would read as the zero
// time and switch the clock guard off.
func TestLoadRefusesAFileWhoseFieldsAreWrong(t *testing.T) {
	st, dir := newStore(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session.json")
	for name, content := range map[string]string{
		"a field of the wrong type":  `{"v":1,"host":5}`,
		"a time that does not parse": `{"v":1,"host":"h","hw":"yesterday"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if sess, err := st.Load(); err == nil || sess != nil {
			t.Errorf("%s: Load = a session %v, %v, want an error", name, sess != nil, err)
		}
	}
}

// A session that cannot be encoded (a time past the year 9999) is an error, and
// nothing is written: the directory is not even created.
func TestSaveReportsASessionItCannotEncodeAndWritesNothing(t *testing.T) {
	st, dir := newStore(t)
	err := st.Save(&account.Session{Host: "h", HW: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("Save of a session that cannot be encoded returned no error")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a Save that failed to encode created %s (stat err %v)", dir, err)
	}
}

// A nil session is a caller's bug, and the gate must not crash a daemon over it:
// Save refuses it with an error and writes nothing, and a session already on disk
// stays as it is.
func TestSaveRefusesANilSessionInsteadOfPanicking(t *testing.T) {
	st, dir := newStore(t)
	if err := st.Save(nil); err == nil || err.Error() != "account: no session to save" {
		t.Fatalf("Save(nil) err = %v, want account: no session to save", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save(nil) created %s (stat err %v)", dir, err)
	}
	if err := st.Save(&account.Session{Host: "h"}); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)
	if err := st.Save(nil); err == nil {
		t.Fatal("Save(nil) with a session on disk returned no error")
	}
	if !reflect.DeepEqual(snapshot(t, dir), before) {
		t.Fatalf("Save(nil) changed the directory (now %v)", names(t, dir))
	}
}

// The first write makes the directory with every missing parent, all private: on
// a fresh machine ~/.monoagent does not exist yet, and Lock is a first write too.
func TestTheFirstWriteCreatesEveryMissingParentPrivately(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name  string
		write func(account.Store) error
	}{
		{"Save", func(s account.Store) error { return s.Save(&account.Session{Host: "h"}) }},
		{"SaveRefresh", func(s account.Store) error { return s.SaveRefresh("rt-1") }},
		{"Lock", func(s account.Store) error {
			unlock, err := s.Lock(context.Background())
			if err == nil {
				unlock()
			}
			return err
		}},
	} {
		first := filepath.Join(root, tc.name)
		dir := filepath.Join(first, ".monoagent", "account")
		if err := tc.write(account.OpenStore(dir, account.NewMemorySealer())); err != nil {
			t.Errorf("%s with missing parent directories: %v", tc.name, err)
			continue
		}
		if runtime.GOOS == "windows" {
			continue
		}
		for _, d := range []string{first, filepath.Join(first, ".monoagent"), dir} {
			fi, err := os.Stat(d)
			if err != nil {
				t.Errorf("%s did not create %s: %v", tc.name, d, err)
				continue
			}
			if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
				t.Errorf("%s created %s with mode %v, want 0700", tc.name, d, fi.Mode().Perm())
			}
		}
	}
}

// The temporary file lives beside its target: a rename across file systems is
// not atomic and may not work at all (~/.monoagent and /tmp are often different
// mounts). Pointing the temporary directory at nothing proves the store does not
// use it.
func TestTheTemporaryFileIsCreatedBesideTheTarget(t *testing.T) {
	st, dir := newStore(t)
	away := filepath.Join(t.TempDir(), "missing")
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(k, away)
	}
	if err := st.Save(&account.Session{Host: "h"}); err != nil {
		t.Fatalf("Save with an unusable temporary directory: %v", err)
	}
	if err := st.SaveRefresh("rt-1"); err != nil {
		t.Fatalf("SaveRefresh with an unusable temporary directory: %v", err)
	}
	if got := names(t, dir); !reflect.DeepEqual(got, []string{"refresh.enc", "session.json"}) {
		t.Fatalf("directory holds %v, want refresh.enc and session.json only", got)
	}
}

// A write that fails is reported and leaves nothing behind: no temporary file,
// and whatever holds the target's name is untouched. The rename fails here
// because the target name is taken by a directory that is not empty.
func TestAFailedWriteIsReportedAndLeavesNoTemporaryFile(t *testing.T) {
	for _, tc := range []struct {
		file  string
		write func(account.Store) error
	}{
		{"session.json", func(s account.Store) error { return s.Save(&account.Session{Host: "h"}) }},
		{"refresh.enc", func(s account.Store) error { return s.SaveRefresh("rt-1") }},
	} {
		st, dir := newStore(t)
		taken := filepath.Join(dir, tc.file, "keep")
		if err := os.MkdirAll(taken, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := tc.write(st); err == nil {
			t.Errorf("%s: a write that could not replace the target returned no error", tc.file)
		}
		if got := names(t, dir); !reflect.DeepEqual(got, []string{tc.file}) {
			t.Errorf("%s: directory holds %v after the failed write, want only %s (no temporary file left)", tc.file, got, tc.file)
		}
		if _, err := os.Stat(taken); err != nil {
			t.Errorf("%s: what held the target's name was touched: %v", tc.file, err)
		}
	}
}

// DeleteRefresh removes the refresh token and nothing else: the session stays.
// What it cannot remove is an error, not silence.
func TestDeleteRefreshRemovesOnlyTheRefreshTokenAndReportsWhatItCannotRemove(t *testing.T) {
	st, dir := newStore(t)
	if err := st.Save(&account.Session{Host: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRefresh("rt-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRefresh(); err != nil {
		t.Fatal(err)
	}
	if got := names(t, dir); !reflect.DeepEqual(got, []string{"session.json"}) {
		t.Fatalf("directory holds %v after DeleteRefresh, want only session.json", got)
	}
	if sess, err := st.Load(); err != nil || sess == nil || sess.Host != "h" {
		t.Fatalf("the session did not survive DeleteRefresh: a session %v, %v", sess != nil, err)
	}

	kept := filepath.Join(dir, "refresh.enc", "keep")
	if err := os.MkdirAll(kept, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRefresh(); err == nil {
		t.Error("DeleteRefresh of a refresh.enc that cannot be removed returned no error")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("DeleteRefresh removed more than the refresh token: %v", err)
	}
}
