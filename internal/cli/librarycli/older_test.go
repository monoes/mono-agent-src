package librarycli

import (
	"bytes"
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// olderRig is the user's own install as the library commands see it, with a fake monoes.me taken
// for the real one: HOME and the home directory the system gives the user are one temporary
// directory, its .monoagent/monoagent.db holds an older library login of the default profile whose
// access token has expired (a read refreshes it first), and nobody has signed in. dbArg is the
// --db-path the commands are given; opened runs once the command has opened it.
type olderRig struct {
	fake         *libraryfake.Server
	home, dbPath string
	dbArg        string
	refresh      string
	opened       func()
	reg          *automation.Registry
	stderr       bytes.Buffer
}

func newOlderRig(t *testing.T) *olderRig {
	t.Helper()
	r := &olderRig{fake: libraryfake.New(), home: t.TempDir()}
	t.Cleanup(r.fake.Close)
	t.Setenv("MONOES_BASE_URL", r.fake.URL)
	account.SetHostForTest(t, r.fake.URL)
	account.SetSealerForTest(t, account.NewMemorySealer())
	libraryfake.TrustKey(t)
	account.SetProductionHostForTest(t, r.fake.URL)
	testhome.SetHome(t, r.home)
	account.SetUserHomeForTest(t, r.home)
	r.dbPath = filepath.Join(r.home, ".monoagent", "monoagent.db")
	r.dbArg = r.dbPath
	if err := os.MkdirAll(filepath.Dir(r.dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	r.refresh = r.seed(t, db)
	if r.reg, err = automation.Open(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return r
}

// seed gives the default profile of db an older login whose access token has expired and returns
// its refresh token.
func (r *olderRig) seed(t *testing.T, db *storage.Database) string {
	t.Helper()
	access, refresh := r.fake.NewGrant("ada")
	tok := &library.Token{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", Method: "pkce", BaseURL: r.fake.URL,
		ExpiresAt: time.Now().Add(-time.Hour), User: &library.User{ID: "u-ada", Username: "ada", Email: "ada@example.com"}}
	if err := (&library.VaultStore{DB: db.DB, ProfileID: "default", BaseURL: r.fake.URL}).Save(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	return refresh
}

func (r *olderRig) deps() Deps {
	return Deps{
		ProfileID: func() string { return "default" },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(clicore.ExpandPath(r.dbArg))
			if r.opened != nil {
				r.opened()
			}
			return db, err
		},
		DBPath:         func() string { return r.dbArg },
		OpenRegistry:   func() (*automation.Registry, error) { return r.reg, nil },
		JSONOutput:     func() bool { return false },
		WithJSONErrors: func(*cobra.Command) {},
		AddSignIn: func(*cobra.Command, string) func(*cobra.Command) (account.Status, bool, error) {
			return nil
		},
		LoginRequired:   func() error { return errLogin },
		IsLoginRequired: func(err error) bool { return errors.Is(err, library.ErrNotLoggedIn) || errors.Is(err, errLogin) },
	}
}

// run runs `library <args>`; its stderr collects in r.stderr.
func (r *olderRig) run(t *testing.T, args ...string) error {
	t.Helper()
	cmd := NewCommand(r.deps())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&r.stderr)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	return cmd.ExecuteContext(t.Context())
}

// sent is every request monoes.me has received.
func (r *olderRig) sent() int {
	n := 0
	for _, c := range r.fake.Requests {
		n += c
	}
	return n
}

// login is the refresh token of the older login the database at path holds, "" when it is gone.
func (r *olderRig) login(t *testing.T, path string) string {
	t.Helper()
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if tok, _ := (&library.VaultStore{DB: db.DB, ProfileID: "default", BaseURL: r.fake.URL}).Load(context.Background()); tok != nil {
		return tok.RefreshToken
	}
	return ""
}

// copyDB copies the user's database to path.
func (r *olderRig) copyDB(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	bindCopy(t, r.dbPath, path)
}

// bindCopy makes the vault of the copy at cp open, as a program that computes the original's
// binding on purpose does (#903): a plain copy no longer opens it, but the guard on the copy's
// logins must hold for one that does.
func bindCopy(t *testing.T, orig, cp string) {
	t.Helper()
	db, err := storage.NewDatabase(cp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.DB.Query(`SELECT profile_id FROM vault_keys`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := secrets.Rebind(context.Background(), db.DB, id, orig); err != nil {
			t.Fatal(err)
		}
	}
}

// #878: `library logout` is open, and a read is open while the gate is dormant. In a copy of the
// user's database, or the database reached from a HOME made for a test, the older login is the
// user's real one: no read sends or refreshes it, logout neither revokes nor removes it and says
// why, and status never looks at it.
func TestOlderLoginIsLeftAloneOutsideTheUsersInstall(t *testing.T) {
	for _, c := range []struct {
		name, why string
		place     func(t *testing.T, r *olderRig) string // moves the user's database or HOME; returns the database the commands use
	}{
		{"a HOME that is not the user's", "the home directory is", func(t *testing.T, r *olderRig) string {
			account.SetUserHomeForTest(t, t.TempDir())
			return r.dbPath
		}},
		{"a copy beside the user's database", "the database is", func(t *testing.T, r *olderRig) string {
			cp := filepath.Join(r.home, "copy.db")
			r.copyDB(t, cp)
			return cp
		}},
		{"a user the system has no home directory for", "the system has no home directory", func(t *testing.T, r *olderRig) string {
			account.SetUserHomeForTest(t, "")
			return r.dbPath
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newOlderRig(t)
			r.dbArg = c.place(t, r)
			if err := r.run(t, "list", "--scope", "official"); !errors.Is(err, errLogin) {
				t.Fatalf("list: %v, want log in first", err)
			}
			if err := r.run(t, "status"); err != nil {
				t.Fatalf("status: %v", err)
			}
			if err := r.run(t, "logout"); err != nil {
				t.Fatalf("logout: %v", err)
			}
			if n := r.sent(); n != 0 {
				t.Fatalf("%d requests reached monoes.me, want none: %v", n, r.fake.Requests)
			}
			if r.login(t, r.dbArg) != r.refresh {
				t.Fatal("the older login is gone")
			}
			if out := r.stderr.String(); strings.Count(out, "neither revoked") != 1 || !strings.Contains(out, c.why) || strings.Contains(out, r.refresh) {
				t.Fatalf("logout must say once why it left the login alone (%q): %q", c.why, out)
			}
		})
	}
}

// The user's own install keeps its older login working: a read refreshes and sends it, and logout
// revokes and removes it.
func TestOlderLoginOnTheUsersOwnInstall(t *testing.T) {
	r := newOlderRig(t)
	r.dbArg = "~/.monoagent/monoagent.db"
	if err := r.run(t, "list", "--scope", "official"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if reqs := r.fake.TokenRequests(); len(reqs) != 1 || reqs[0].Form.Get("refresh_token") != r.refresh {
		t.Fatalf("%d token requests, want the older login's refresh", len(reqs))
	}
	if err := r.run(t, "logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if n := r.fake.Requests["POST /api/auth/oauth2/revoke"]; n != 1 || r.login(t, r.dbPath) != "" {
		t.Fatalf("logout: %d revocations, login still there %v", n, r.login(t, r.dbPath) != "")
	}
	if strings.Contains(r.stderr.String(), "neither revoked") {
		t.Fatalf("the user's own logout said it left the login alone: %q", r.stderr.String())
	}
}

// The older login is read from the file the place check found, not from the database the command
// opened: a --db-path that pointed at a copy when it was opened and at the user's database when it
// was checked does not get the copy's login sent.
func TestOlderLoginIsReadFromTheFileItChecked(t *testing.T) {
	r := newOlderRig(t)
	cp := filepath.Join(r.home, "copy.db")
	r.copyDB(t, cp)
	db, err := storage.NewDatabase(cp)
	if err != nil {
		t.Fatal(err)
	}
	copied := r.seed(t, db)
	db.Close()
	link := filepath.Join(t.TempDir(), "db")
	if err := os.Symlink(cp, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	r.dbArg = link
	r.opened = func() {
		if err := os.Remove(link); err != nil {
			t.Error(err)
		}
		if err := os.Symlink(r.dbPath, link); err != nil {
			t.Error(err)
		}
	}
	if err := r.run(t, "list", "--scope", "official"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if reqs := r.fake.TokenRequests(); len(reqs) != 1 || reqs[0].Form.Get("refresh_token") != r.refresh {
		t.Fatalf("%d token requests; the one sent is the user's own login: %v", len(reqs), len(reqs) == 1 && reqs[0].Form.Get("refresh_token") == r.refresh)
	}
	if r.login(t, cp) != copied {
		t.Fatal("the copy's login was touched")
	}
}
