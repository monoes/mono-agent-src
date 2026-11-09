package account_test

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
)

// sessionRig is the user's own install signed in at a fake monoes.me taken for the real one: HOME
// and the home directory the system gives the user are one temporary directory, its
// .monoagent/account holds a session whose two-minute access token is due for a refresh, and every
// store seals with one key, as the OS key store would for any copy the same user makes.
type sessionRig struct {
	fake    *libraryfake.Server
	sealer  account.Sealer
	home    string
	refresh string // the stored refresh token
	signIn  int    // the refresh grants the sign-in itself took
	signReq int    // and the token requests it made
}

func newSessionRig(t *testing.T) *sessionRig {
	t.Helper()
	r := &sessionRig{fake: libraryfake.New(), sealer: account.NewMemorySealer(), home: t.TempDir()}
	t.Cleanup(r.fake.Close)
	r.fake.AccessTTL = 2 * time.Minute // under RefreshMargin: a command refreshes it
	libraryfake.TrustKey(t)
	account.SetHostForTest(t, r.fake.URL)
	account.SetProductionHostForTest(t, r.fake.URL)
	account.SetSealerForTest(t, r.sealer)
	r.at(t, r.home)
	account.SetUserHomeForTest(t, r.home)
	store, err := account.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	_, rt := r.fake.NewGrant("ada")
	if res, err := account.NewClient(r.fake.URL, store).Adopt(context.Background(), olderLogin(rt), nil); err != nil || !res.Adopted {
		t.Fatalf("signing in: adopted %v, %v", res.Adopted, err)
	}
	if r.refresh, err = store.LoadRefresh(); err != nil || r.refresh == "" {
		t.Fatalf("no refresh token stored: %v", err)
	}
	r.signIn, r.signReq = r.fake.Refreshes, len(r.fake.TokenRequests())
	return r
}

// refreshes is the refresh grants monoes.me honoured since the sign-in.
func (r *sessionRig) refreshes() int { return r.fake.Refreshes - r.signIn }

// sent is every token request since the sign-in, honoured or not.
func (r *sessionRig) sent() int { return len(r.fake.TokenRequests()) - r.signReq }

// at makes home the HOME of the process.
func (r *sessionRig) at(t *testing.T, home string) {
	t.Helper()
	testhome.SetHome(t, home)
}

// accountDir is the account folder under home.
func accountDir(home string) string { return filepath.Join(home, ".monoagent", "account") }

// copyTo copies the user's account folder to home, as an agent that copies ~/.monoagent would.
func (r *sessionRig) copyTo(t *testing.T, home string) {
	t.Helper()
	if err := os.CopyFS(accountDir(home), os.DirFS(accountDir(r.home))); err != nil {
		t.Fatal(err)
	}
}

// refresh is a command's refresh by the production guard (NewDefaultGuard's store and refresher),
// 90 seconds on: past the negative cache of the sign-in, inside the access token's margin.
func refresh(t *testing.T) {
	t.Helper()
	store, err := account.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	g := account.NewGuard(account.GuardOptions{Store: store, Refresher: account.NewRefresher(account.Host()),
		Now: func() time.Time { return time.Now().Add(90 * time.Second) }})
	defer g.Close()
	_, _ = g.Refresh(context.Background())
}

// refreshAndLogOut runs what presents the refresh token: a command's refresh, then a logout.
func refreshAndLogOut(t *testing.T) {
	t.Helper()
	refresh(t)
	if err := account.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (r *sessionRig) revocations() int { return r.fake.Requests["POST /api/auth/oauth2/revoke"] }

// #878: a copy of the account folder holds the user's real refresh token, and the key store opens
// it. The token names the folder it was sealed for, so in a copy it is not read: no refresh is
// sent and logout revokes nothing, even where HOME passes for the user's own. The original keeps
// working, and nothing was presented twice.
func TestACopiedSessionIsNeitherRenewedNorRevoked(t *testing.T) {
	for _, c := range []struct {
		name string
		home func(t *testing.T, r *sessionRig, other string)
	}{
		{"a HOME that is not the user's", func(*testing.T, *sessionRig, string) {}},
		{"a HOME that passes for the user's", func(t *testing.T, _ *sessionRig, other string) {
			account.SetUserHomeForTest(t, other)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newSessionRig(t)
			other := t.TempDir()
			r.copyTo(t, other)
			r.at(t, other)
			c.home(t, r, other)
			refreshAndLogOut(t)
			if r.sent() != 0 || r.revocations() != 0 {
				t.Fatalf("the copy sent %d token requests and %d revocations, want none", r.sent(), r.revocations())
			}
			r.at(t, r.home)
			account.SetUserHomeForTest(t, r.home)
			refreshAndLogOut(t)
			if r.refreshes() != 1 || r.revocations() != 1 || r.fake.Replays != 0 {
				t.Fatalf("the original: %d refreshes, %d revocations, %d replays; want 1, 1, 0", r.refreshes(), r.revocations(), r.fake.Replays)
			}
		})
	}
}

// A refresh token sealed before #878 names no folder. It is read only in the user's own account
// folder, where the next refresh seals its successor for that folder; a copy of it elsewhere, or
// a user the system has no home directory for, sends nothing.
func TestARefreshTokenThatNamesNoFolder(t *testing.T) {
	legacy := func(t *testing.T, r *sessionRig, home string) {
		t.Helper()
		blob, err := r.sealer.Seal([]byte(r.refresh))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(accountDir(home), "refresh.enc"), blob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("in the user's own folder", func(t *testing.T) {
		r := newSessionRig(t)
		legacy(t, r, r.home)
		refresh(t)
		if r.refreshes() != 1 || r.fake.Replays != 0 {
			t.Fatalf("%d refreshes, %d replays; want 1 and 0", r.refreshes(), r.fake.Replays)
		}
		blob, err := os.ReadFile(filepath.Join(accountDir(r.home), "refresh.enc"))
		if err != nil {
			t.Fatal(err)
		}
		if plain, err := r.sealer.Open(blob); err != nil || !strings.HasPrefix(string(plain), "monoagent-account-v3\x00") {
			t.Fatalf("the successor names no folder (%v)", err)
		}
	})
	for _, c := range []struct {
		name string
		move func(t *testing.T, r *sessionRig)
	}{
		{"a copy in another HOME", func(t *testing.T, r *sessionRig) {
			other := t.TempDir()
			r.copyTo(t, other)
			legacy(t, r, other)
			r.at(t, other)
		}},
		{"a user the system has no home directory for", func(t *testing.T, r *sessionRig) {
			legacy(t, r, r.home)
			account.SetUserHomeForTest(t, "")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newSessionRig(t)
			c.move(t, r)
			refreshAndLogOut(t)
			if r.sent() != 0 || r.revocations() != 0 {
				t.Fatalf("%d token requests and %d revocations, want none", r.sent(), r.revocations())
			}
		})
	}
}

// A sign-in made in a HOME that is not the user's own, or by a user the system has no home
// directory for, is that HOME's own: its refresh token names its folder, so it is renewed and
// revoked there as anywhere. Only a copy is refused.
func TestASignInMadeOutsideTheUsersHomeIsItsOwn(t *testing.T) {
	r := newSessionRig(t)
	account.SetUserHomeForTest(t, "")
	refreshAndLogOut(t)
	if r.refreshes() != 1 || r.revocations() != 1 || r.fake.Replays != 0 {
		t.Fatalf("%d refreshes, %d revocations, %d replays; want 1, 1, 0", r.refreshes(), r.revocations(), r.fake.Replays)
	}
}

// A folder renamed or moved on its filesystem keeps its device and inode: its sign-in still renews
// there (a renamed home, a volume mounted at another path), and the successor names the new path.
// A copy gets a new inode and stays refused (TestACopiedSessionIsNeitherRenewedNorRevoked).
func TestARenamedAccountFolderStillRenews(t *testing.T) {
	r := newSessionRig(t)
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(r.home, moved); err != nil {
		t.Fatal(err)
	}
	r.at(t, moved)
	account.SetUserHomeForTest(t, moved)
	refresh(t)
	if r.refreshes() != 1 || r.fake.Replays != 0 {
		t.Fatalf("%d refreshes, %d replays; want 1 and 0", r.refreshes(), r.fake.Replays)
	}
	blob, err := os.ReadFile(filepath.Join(accountDir(moved), "refresh.enc"))
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(accountDir(moved))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := r.sealer.Open(blob); err != nil || !strings.HasPrefix(string(plain), "monoagent-account-v3\x00"+real+"\x00") {
		t.Fatalf("the successor does not name the folder's new path (%v)", err)
	}
}

// A sealed token of another version of the format (a build of this branch before v3 named the
// path only) is refused, never presented as if its plaintext were the token.
func TestAnotherVersionOfTheSealedFormatIsRefused(t *testing.T) {
	r := newSessionRig(t)
	blob, err := r.sealer.Seal([]byte("monoagent-account-v2\x00" + accountDir(r.home) + "\x00" + r.refresh))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountDir(r.home), "refresh.enc"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	refreshAndLogOut(t)
	if r.sent() != 0 || r.revocations() != 0 {
		t.Fatalf("%d token requests and %d revocations, want none", r.sent(), r.revocations())
	}
}

// A home renamed away and a copy of it put back at the sealed path (`mv ~/.monoagent{,.bak}` and a
// restore): when both identities are known they decide, so the renamed original renews and the
// copy at the sealed path does not. Only one of them ever presents the token.
func TestACopyRestoredAtTheSealedPathAfterARename(t *testing.T) {
	r := newSessionRig(t)
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(r.home, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(accountDir(r.home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(accountDir(r.home), os.DirFS(accountDir(moved))); err != nil {
		t.Fatal(err)
	}
	refresh(t) // HOME is still the sealed path: the restored copy
	if r.sent() != 0 {
		t.Fatalf("the copy at the sealed path sent %d token requests, want none", r.sent())
	}
	r.at(t, moved)
	account.SetUserHomeForTest(t, moved)
	refresh(t)
	if r.refreshes() != 1 || r.sent() != 1 || r.fake.Replays != 0 {
		t.Fatalf("the renamed original: %d refreshes, %d token requests, %d replays; want 1, 1, 0", r.refreshes(), r.sent(), r.fake.Replays)
	}
}
