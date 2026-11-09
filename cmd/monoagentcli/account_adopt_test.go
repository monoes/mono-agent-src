package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// adoptRig is a migrated database and a counter in place of the library's adoption: what the wiring
// decides is whether to call it, and internal/library proves what the adoption does.
type adoptRig struct {
	dbPath string
	calls  atomic.Int32
}

func newAdoptRig(t *testing.T, mode accounttest.Mode, afterTheDate bool) *adoptRig {
	t.Helper()
	accounttest.Install(t, mode)
	if mode == accounttest.LockedNoLogin && !afterTheDate {
		account.SetEnforceFromForTest(t, time.Now().Add(48*time.Hour))
	}
	r := &adoptRig{dbPath: ownInstallDB(t)}
	was := adoptOlderLogin
	adoptOlderLogin = func(context.Context, *sql.DB, *account.Guard, func(string, library.Outcome)) (bool, error) {
		r.calls.Add(1)
		return false, nil
	}
	t.Cleanup(func() { adoptOlderLogin = was })
	return r
}

// ownInstallDB is a migrated database where this OS user's own install keeps it, the one place a
// monoes.me login is adopted from (#878): HOME and the home directory the system gives the user are
// one temporary directory, and the database is its .monoagent/monoagent.db.
func ownInstallDB(t *testing.T) string {
	t.Helper()
	home := freshHome(t)
	account.SetUserHomeForTest(t, home)
	path := filepath.Join(home, ".monoagent", "monoagent.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(testdb.Path(t), path); err != nil {
		t.Fatal(err)
	}
	return path
}

func adoptionCommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	root := newRootCmd()
	applyClassification(root)
	c, _, err := root.Find(args)
	if err != nil || c == root {
		t.Fatalf("no command %v: %v", args, err)
	}
	return c
}

func TestAdoptFirstRun(t *testing.T) {
	for _, c := range []struct {
		name    string
		mode    accounttest.Mode
		after   bool
		args    []string
		noDB    bool
		noGuard bool
		want    int32
	}{
		{"a gated command on a machine with no session", accounttest.LockedNoLogin, false, []string{"workflow", "list"}, false, false, 1},
		{"a serving command after the date", accounttest.LockedNoLogin, true, []string{"daemon"}, false, false, 1},
		{"an open command", accounttest.LockedNoLogin, false, []string{"version"}, false, false, 0},
		{"the gate is dormant", accounttest.Dormant, false, []string{"workflow", "list"}, false, false, 0},
		{"a session exists", accounttest.SignedIn, false, []string{"workflow", "list"}, false, false, 0},
		{"a session that monoes.me refused", accounttest.LockedRefused, false, []string{"workflow", "list"}, false, false, 0},
		{"no database yet", accounttest.LockedNoLogin, false, []string{"workflow", "list"}, true, false, 0},
		{"no guard installed", accounttest.LockedNoLogin, false, []string{"workflow", "list"}, false, true, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newAdoptRig(t, c.mode, c.after)
			if c.noDB {
				r.dbPath = filepath.Join(t.TempDir(), "absent.db")
			}
			if c.noGuard {
				account.Install(nil)
			}
			adoptFirstRun(adoptionCommand(t, c.args...), &globalConfig{DBPath: r.dbPath})
			if got := r.calls.Load(); got != c.want {
				t.Fatalf("the adoption was called %d times, want %d", got, c.want)
			}
			if _, err := os.Stat(r.dbPath); c.noDB && !os.IsNotExist(err) {
				t.Fatalf("the wiring created a database for nothing: %v", err)
			}
		})
	}
}

// A try is made once per database, ever, and two processes that start together make one between them.
func TestAdoptFirstRunTriesOnce(t *testing.T) {
	r := newAdoptRig(t, accounttest.LockedNoLogin, false)
	cfg, cmd := &globalConfig{DBPath: r.dbPath}, adoptionCommand(t, "workflow", "list")
	for i := 0; i < 3; i++ {
		adoptFirstRun(cmd, cfg)
	}
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("three runs made %d tries, want 1", got)
	}

	r = newAdoptRig(t, accounttest.LockedNoLogin, false)
	cfg = &globalConfig{DBPath: r.dbPath}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); adoptFirstRun(cmd, cfg) }()
	}
	wg.Wait()
	if got := r.calls.Load(); got != 1 {
		t.Fatalf("eight processes starting together made %d tries, want 1", got)
	}
}

// An adoption that fails is not repeated and does not stop the command; one line says it failed.
func TestAdoptFirstRunFailureIsClaimedAndSwallowed(t *testing.T) {
	r := newAdoptRig(t, accounttest.LockedNoLogin, false)
	var calls atomic.Int32
	adoptOlderLogin = func(context.Context, *sql.DB, *account.Guard, func(string, library.Outcome)) (bool, error) {
		calls.Add(1)
		return false, errors.New("boom")
	}
	cfg, cmd := &globalConfig{DBPath: r.dbPath}, adoptionCommand(t, "workflow", "list")
	var errb bytes.Buffer
	cmd.SetErr(&errb)
	adoptFirstRun(cmd, cfg)
	adoptFirstRun(cmd, cfg)
	if got := calls.Load(); got != 1 {
		t.Fatalf("a failed try was made %d times, want 1", got)
	}
	if got := errb.String(); got != "Adopting an earlier library login failed: boom\n" {
		t.Fatalf("output %q", got)
	}
}

// The wiring itself, through run() as main does: the gate judges before cobra runs any hook, so the
// older login must be adopted ahead of it. With the enforce date past and no session, a gated command
// is refused unless the adoption made one first, and its line reaches stderr; an open command makes
// no try. Fails when the call in main.go is removed.
func TestRunAdoptsBeforeTheGateJudges(t *testing.T) {
	f := accounttest.New(t) // trusts its key, enforces from a date in the past
	dbPath := ownInstallDB(t)
	store := account.OpenStore(t.TempDir(), account.NewMemorySealer())
	prevGuard := newDefaultGuard
	newDefaultGuard = func() (*account.Guard, error) {
		return account.NewGuard(account.GuardOptions{Store: store, Now: f.Clock.Now}), nil
	}
	t.Cleanup(func() { newDefaultGuard = prevGuard })
	var calls atomic.Int32
	was := adoptOlderLogin
	adoptOlderLogin = func(_ context.Context, _ *sql.DB, _ *account.Guard, report func(string, library.Outcome)) (bool, error) {
		calls.Add(1)
		report("default", library.OutcomeAdopted)
		now := f.Clock.Now()
		sess, err := account.NewSession(account.HostURL, f.Token(accounttest.TokenOptions{IssuedAt: now}),
			&account.User{ID: "u", Email: "u@example.test", Username: "u"}, now)
		if err != nil {
			t.Error(err)
			return false, err
		}
		return true, store.Save(sess)
	}
	t.Cleanup(func() { adoptOlderLogin = was })

	if code, _, _ := runMain(t, "--db-path", dbPath, "version"); code != 0 || calls.Load() != 0 {
		t.Fatalf("an open command: code %d, %d tries, want 0 and 0", code, calls.Load())
	}
	code, _, errb := runMain(t, "--db-path", dbPath, "--profile", "default", "workflow", "list")
	if calls.Load() != 1 {
		t.Fatalf("the adoption was called %d times, want 1", calls.Load())
	}
	if code != 0 {
		t.Fatalf("the gated command was refused after the adoption (code %d): %s", code, errb)
	}
	if !strings.Contains(errb, "Signed in to "+account.HostURL+" with the earlier library login of profile \"default\".") {
		t.Fatalf("the adoption's line is not on stderr: %q", errb)
	}
	runMain(t, "--db-path", dbPath, "--profile", "default", "workflow", "list")
	if calls.Load() != 1 {
		t.Fatalf("a second command tried again: %d calls", calls.Load())
	}
}

// A machine that has had its try, or has no login to adopt, is not written to by later commands.
func TestAdoptFirstRunReadsBeforeItClaims(t *testing.T) {
	r := newAdoptRig(t, accounttest.LockedNoLogin, false)
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(adoptionSetting, "earlier"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	adoptFirstRun(adoptionCommand(t, "workflow", "list"), &globalConfig{DBPath: r.dbPath})
	if r.calls.Load() != 0 {
		t.Fatal("a database that had its try was tried again")
	}
}

// realAdoptRig runs the library's real adoption against a home with no monoes.me behind it: any call
// to the network fails, so a test that passes proved the wiring and the adoption sent nothing.
func realAdoptRig(t *testing.T) (*adoptRig, string) {
	t.Helper()
	r := newAdoptRig(t, accounttest.LockedNoLogin, false)
	adoptOlderLogin = library.AdoptIntoAccount
	home := t.TempDir()
	testhome.SetHome(t, home)
	account.SetHostForTest(t, "http://127.0.0.1:1")
	account.SetEnforceFromForTest(t, time.Now().Add(-time.Hour))
	return r, home
}

func noAccountFolder(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "account")); !os.IsNotExist(err) {
		t.Fatalf("the adoption made an account folder: %v", err)
	}
}

// An unreadable older login is nobody's login: nothing is adopted or stored, and the failed look is
// not repeated.
func TestAdoptFirstRunWithACorruptOlderLoginIsInert(t *testing.T) {
	r, home := realAdoptRig(t)
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var pid string
	if err := db.DB.QueryRow(`SELECT id FROM profiles LIMIT 1`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Add(context.Background(), db.DB, pid, "secret", library.VaultSecretName,
		map[string]string{"token": "{not json"}, "u", "http://127.0.0.1:1", ""); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cfg, cmd := &globalConfig{DBPath: r.dbPath}, adoptionCommand(t, "workflow", "list")
	adoptFirstRun(cmd, cfg)
	adoptFirstRun(cmd, cfg)
	noAccountFolder(t, home)
}

// With no older login at all the wiring writes only its claim row.
func TestAdoptFirstRunIsInertWithoutAnOlderLogin(t *testing.T) {
	r, home := realAdoptRig(t)
	adoptFirstRun(adoptionCommand(t, "workflow", "list"), &globalConfig{DBPath: r.dbPath})
	noAccountFolder(t, home)
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, _ := db.GetSetting(adoptionSetting); v == "" {
		t.Fatal("the try was not claimed")
	}
}
