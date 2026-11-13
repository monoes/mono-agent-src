package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// olderRig is a home with a migrated database whose default profile holds an older library login,
// written by the library's own VaultStore (what the previous release stored), an enforced gate with no
// session, and the account host pointed at host. The adoption under test is the real one.
type olderRig struct {
	home   string
	dbPath string
	vault  *library.VaultStore
	fake   *libraryfake.Server
}

func newOlderRig(t *testing.T, host func(*libraryfake.Server) string) *olderRig {
	t.Helper()
	keyring.MockInit()
	home := freshHome(t)
	fake := libraryfake.New()
	t.Cleanup(fake.Close)
	libraryfake.TrustKey(t)
	h := host(fake)
	account.SetHostForTest(t, h)
	account.SetSealerForTest(t, account.NewMemorySealer())
	account.SetEnforceFromForTest(t, time.Now().Add(-time.Hour))

	dbPath := testdb.Path(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	access, refresh := fake.NewGrant("ada")
	vault := &library.VaultStore{DB: db.DB, ProfileID: "default", BaseURL: h}
	tok := &library.Token{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", Method: "pkce", BaseURL: h,
		User: &library.User{ID: "u-ada", Username: "ada", Email: "ada@example.com"}}
	if err := vault.Save(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	return &olderRig{home: home, dbPath: dbPath, vault: vault, fake: fake}
}

func fakeHost(f *libraryfake.Server) string { return f.URL }

// gated runs a gated command through run(), as main does.
func (r *olderRig) gated(t *testing.T) (code int, stderr string) {
	t.Helper()
	code, _, stderr = runMain(t, "--db-path", r.dbPath, "--profile", "default", "workflow", "list")
	return code, stderr
}

func (r *olderRig) olderLoginKept(t *testing.T) bool {
	t.Helper()
	tok, _ := r.vault.Load(context.Background())
	return tok != nil
}

// signedIn says whether the account store holds somebody's login.
func signedIn(t *testing.T) bool {
	t.Helper()
	store, err := account.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Load()
	return err == nil && sess != nil && sess.AccessToken != ""
}

func (r *olderRig) claimed(t *testing.T) bool {
	t.Helper()
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, _ := db.GetSetting(adoptionSetting)
	return v != ""
}

func (r *olderRig) sessionFilesAre0600(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return // Windows has no permission bits: the files' owner-only access is an ACL (internal/account's own tests)
	}
	dir := filepath.Join(r.home, ".monoagent", "account")
	files := filesUnder(t, dir)
	if len(files) == 0 {
		t.Fatal("the adoption stored no session")
	}
	for _, f := range files {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %o, want 600", f, fi.Mode().Perm())
		}
	}
}

// A valid older session is adopted by the first gated command, once: the account session is stored
// with owner-only permissions, the older vault entry goes, and a second run presents nothing.
func TestRunAdoptsARealOlderLogin(t *testing.T) {
	r := newOlderRig(t, fakeHost)
	if code, stderr := r.gated(t); code != 0 {
		t.Fatalf("the gated command was refused (code %d): %s", code, stderr)
	}
	if r.fake.Refreshes != 1 {
		t.Fatalf("%d refresh grants, want 1", r.fake.Refreshes)
	}
	if !signedIn(t) {
		t.Fatal("no account session")
	}
	r.sessionFilesAre0600(t)
	if r.olderLoginKept(t) {
		t.Fatal("the adopted login is still in the vault")
	}
	if code, stderr := r.gated(t); code != 0 || r.fake.Refreshes != 1 || r.fake.Replays != 0 {
		t.Fatalf("second run: code %d, refreshes %d, replays %d: %s", code, r.fake.Refreshes, r.fake.Replays, stderr)
	}
}

// An older login monoes.me says is dead is removed (an older binary would present it again and end
// every login of the account) and nothing is stored; the command is asked to log in, and the try is
// not repeated.
func TestRunDropsADeadOlderLogin(t *testing.T) {
	r := newOlderRig(t, fakeHost)
	r.fake.SetRefreshMode(libraryfake.RefreshInvalidGrant)
	if code, _ := r.gated(t); code == 0 {
		t.Fatal("the gated command ran without a login")
	}
	if signedIn(t) {
		t.Fatal("a dead login became a session")
	}
	if r.olderLoginKept(t) {
		t.Fatal("the dead login is still in the vault")
	}
	asked := len(r.fake.TokenRequests())
	r.gated(t)
	if len(r.fake.TokenRequests()) != asked || r.fake.Replays != 0 {
		t.Fatal("a second run presented a token")
	}
}

// With monoes.me unreachable nothing is sent, so the older login stays as it was. The try is claimed
// all the same and is not repeated: the command asks for a login instead.
func TestRunKeepsTheOlderLoginWhenMonoesMeIsUnreachable(t *testing.T) {
	r := newOlderRig(t, fakeHost)
	r.fake.Close()
	if code, _ := r.gated(t); code == 0 {
		t.Fatal("the gated command ran without a login")
	}
	if !r.olderLoginKept(t) {
		t.Fatal("the older login was removed though nothing was sent")
	}
	if signedIn(t) {
		t.Fatal("a session appeared")
	}
	if !r.claimed(t) {
		t.Fatal("the try was not claimed")
	}
}

// A token endpoint that answers the refresh grant with a 307 to another host must not be followed:
// the refresh token would be re-sent there (the fix of PR 421).
func TestRunDoesNotFollowARedirectWithTheRefreshToken(t *testing.T) {
	var stolen, hits, redirected atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		hits.Add(1)
		if b, _ := io.ReadAll(req.Body); len(b) > 0 {
			stolen.Add(1)
		}
	}))
	defer elsewhere.Close()

	r := newOlderRig(t, func(f *libraryfake.Server) string {
		target, _ := url.Parse(f.URL)
		proxy := httputil.NewSingleHostReverseProxy(target)
		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			switch {
			case req.URL.Path == "/.well-known/oauth-authorization-server":
				http.NotFound(w, req) // the refresher falls back to the default endpoints, on this host
			case req.Method == http.MethodPost && req.URL.Path == "/api/auth/oauth2/token":
				redirected.Add(1)
				http.Redirect(w, req, elsewhere.URL+"/collect", http.StatusTemporaryRedirect)
			default:
				proxy.ServeHTTP(w, req)
			}
		}))
		t.Cleanup(front.Close)
		return front.URL
	})
	if code, _ := r.gated(t); code == 0 {
		t.Fatal("the gated command ran without a login")
	}
	if redirected.Load() == 0 {
		t.Fatal("the refresh grant never reached the redirecting endpoint")
	}
	if hits.Load() != 0 || stolen.Load() != 0 {
		t.Fatalf("the redirect was followed: %d requests, %d with a body", hits.Load(), stolen.Load())
	}
	if signedIn(t) {
		t.Fatal("a redirect produced a session")
	}
}
