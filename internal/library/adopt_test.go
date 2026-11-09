package library_test

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// adoptFixture is a fake monoes.me, a migrated database whose default profile
// holds an older library login (an opaque token and a refresh token, in its vault),
// an enforced gate, and a home of its own for the session.
type adoptFixture struct {
	fake  *libraryfake.Server
	db    *storage.Database
	vault *library.VaultStore
	guard *account.Guard
}

func newAdoptFixture(t *testing.T) *adoptFixture {
	t.Helper()
	keyring.MockInit()
	home := t.TempDir()
	testhome.SetHome(t, home)
	fake := libraryfake.New()
	t.Cleanup(fake.Close)
	libraryfake.TrustKey(t)
	account.SetHostForTest(t, fake.URL)
	account.SetSealerForTest(t, account.NewMemorySealer())
	account.SetEnforceFromForTest(t, time.Now().Add(-time.Hour))
	db := testdb.Open(t)
	vault := &library.VaultStore{DB: db.DB, ProfileID: "default", BaseURL: fake.URL}
	c := mustClient(t, fake.URL, vault)
	if _, err := c.LoginPKCE(context.Background(), library.LoginOptions{Open: browser(t), Timeout: 10 * time.Second}); err != nil {
		t.Fatalf("the older login: %v", err)
	}
	g, err := account.NewDefaultGuard()
	if err != nil {
		t.Fatal(err)
	}
	return &adoptFixture{fake: fake, db: db, vault: vault, guard: g}
}

// addProfile makes a second profile whose vault holds an older login of its own.
func (f *adoptFixture) addProfile(t *testing.T, id string) *library.VaultStore {
	t.Helper()
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	if _, err := f.db.DB.Exec(`INSERT INTO profiles (id, name, created_at, root_dir, icon) VALUES (?, ?, ?, ?, ?)`, id, id, now, "", ""); err != nil {
		t.Fatal(err)
	}
	access, refresh := f.fake.NewGrant("ada")
	vs := &library.VaultStore{DB: f.db.DB, ProfileID: id, BaseURL: f.fake.URL}
	tok := &library.Token{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", Method: "pkce", BaseURL: f.fake.URL,
		User: &library.User{ID: "u-ada", Username: "ada", Email: "ada@example.com"}}
	if err := vs.Save(context.Background(), tok); err != nil {
		t.Fatal(err)
	}
	return vs
}

func hostOf(s *account.Session) string {
	if s == nil {
		return ""
	}
	return s.Host
}

func (f *adoptFixture) session(t *testing.T) *account.Session {
	t.Helper()
	store, err := account.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestAdoptTurnsAnOlderLoginIntoTheSession(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil)
	if err != nil || !adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if sess := f.session(t); sess == nil || sess.Host != f.fake.URL || sess.User == nil || sess.User.Username != "ada" {
		t.Fatalf("session: present %v, host %q", sess != nil, hostOf(sess))
	}
	if tok, _ := f.vault.Load(ctx); tok != nil {
		t.Fatal("the adopted login is still in the vault")
	}
	if g, err := account.NewDefaultGuard(); err != nil || g.Status().State != account.StateOK {
		t.Fatalf("a new guard does not see the session: %v", err)
	}
	before := f.fake.Refreshes
	if again, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || again || f.fake.Refreshes != before {
		t.Fatalf("a second run: adopted %v, %v, refreshes %d -> %d", again, err, before, f.fake.Refreshes)
	}
	if f.fake.Replays != 0 {
		t.Fatal("the spent refresh token was presented again: monoes.me would end every login of the account")
	}
}

// Spec D22: while dormant nothing is called implicitly.
func TestAdoptIsDormantUntilTheGateIs(t *testing.T) {
	f := newAdoptFixture(t)
	account.SetEnforceFromForTest(t, time.Time{})
	if adopted, err := library.AdoptIntoAccount(context.Background(), f.db.DB, f.guard, nil); err != nil || adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if f.fake.Refreshes != 0 {
		t.Fatalf("a dormant adoption called monoes.me (%d refreshes)", f.fake.Refreshes)
	}
	if tok, _ := f.vault.Load(context.Background()); tok == nil {
		t.Fatal("the older login was touched")
	}
}

// Spec D23: whatever monoes.me answers, the outcome is "sign in once more", never a
// refusal. An invalid_grant says the older refresh token is dead; presenting it again
// from an older binary would end every session of the account, the new one included,
// so the vault entry goes, and a second call has nothing left to present.
func TestAdoptNeverReadsAnAnswerAsARefusalAndDropsADeadLogin(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	f.fake.SetRefreshMode(libraryfake.RefreshInvalidGrant)
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || adopted {
		t.Fatalf("invalid_grant: adopted %v, %v", adopted, err)
	}
	if g, err := account.NewDefaultGuard(); err != nil || g.Status().Reason != account.ReasonNotLoggedIn || f.session(t) != nil {
		t.Fatalf("an answer to the adoption was read as a refusal (%v)", err)
	}
	if tok, _ := f.vault.Load(ctx); tok != nil {
		t.Fatal("the dead login is still in the vault: an older binary would present it again")
	}
	asked := len(f.fake.TokenRequests())
	if again, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || again || len(f.fake.TokenRequests()) != asked {
		t.Fatalf("a second call presented something (adopted %v, %v)", again, err)
	}
	if f.fake.Replays != 0 {
		t.Fatal("a refresh token was presented after it was spent")
	}
}

// monoes.me answering with a complete 4xx that is not invalid_grant (here an unknown
// client; a rate limit or a malformed request alike) processed nothing, and not being
// reached at all sent nothing: no verdict, every older login stays as it was, and the
// call asks once (a connect timeout per profile would be paid at the first command of a
// machine that is offline). A request that went out and got any other answer, a 500
// included, or none, is another matter: see TestAdoptDropsTheOlderLoginWhenTheAnswerIsLost.
func TestAdoptKeepsTheOlderLoginsAfterATransientFailureAndAsksOnce(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	second := f.addProfile(t, "second-profile")
	first, _ := f.vault.Load(ctx)
	asked := len(f.fake.TokenRequests())
	f.fake.SetRefreshMode(libraryfake.RefreshInvalidClient)
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || adopted {
		t.Fatalf("a failing monoes.me: adopted %v, %v", adopted, err)
	}
	if kept, _ := f.vault.Load(ctx); kept == nil || kept.RefreshToken != first.RefreshToken {
		t.Fatal("a login monoes.me gave no verdict on was changed or removed")
	}
	if other, _ := second.Load(ctx); other == nil {
		t.Fatal("the second profile's login was removed")
	}
	if n := len(f.fake.TokenRequests()) - asked; n != 1 || f.session(t) != nil {
		t.Fatalf("%d token requests, session present %v: want one request and no session", n, f.session(t) != nil)
	}
}

// A25: from the enforcement date the guard's first pass on a machine that never signed in writes a session
// with no token, the clock-guard record. The gate lets a serving command through while locked and makes that
// pass just before B5a's wiring calls the adoption, so the adoption must go ahead over the record: an older
// library login is still adopted, and the session replaces the record.
func TestAdoptStillGoesAheadAfterTheGuardHasWrittenItsRecord(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	if _, err := f.guard.EnsureFresh(ctx); err != nil {
		t.Fatal(err)
	}
	if sess := f.session(t); sess == nil || sess.AccessToken != "" || sess.HW.IsZero() {
		t.Fatalf("the guard's pass left a session %v, want the record of a machine that never signed in: no token, a high-water mark", sess != nil)
	}
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || !adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if sess := f.session(t); sess == nil || sess.AccessToken == "" || f.fake.Replays != 0 {
		t.Fatalf("the session did not replace the record (session present %v, replays %d)", sess != nil, f.fake.Replays)
	}
}

// A24: the exchange went out and its outcome is unknown: monoes.me may have rotated the older refresh
// token and lost the answer that held its successor (RefreshLost does exactly that; the others leave the
// token good, and the client cannot tell the difference: a 500 can come after the rotation was committed).
// An older binary, or a later call, would present it again after monoes.me's 300-second reuse window, which
// ends every refresh token of the account, so the vault entry goes, nothing is stored, and the call stops
// there: the next profile's login waits, and the call asks once.
func TestAdoptDropsTheOlderLoginWhenTheAnswerIsLost(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode libraryfake.RefreshMode
	}{
		{"the token is rotated and the answer is lost", libraryfake.RefreshLost},
		{"the connection closes after the request was read", libraryfake.RefreshDrop},
		{"an answer that is not a token set", libraryfake.RefreshMalformed},
		{"a 500", libraryfake.RefreshServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdoptFixture(t)
			ctx := context.Background()
			second := f.addProfile(t, "second-profile")
			asked := len(f.fake.TokenRequests())
			f.fake.SetRefreshMode(tc.mode)
			if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || adopted {
				t.Fatalf("adopted %v, %v", adopted, err)
			}
			if tok, _ := f.vault.Load(ctx); tok != nil {
				t.Fatal("the older login that monoes.me may have spent is still in the vault: an older binary would present it again")
			}
			if other, _ := second.Load(ctx); other == nil {
				t.Fatal("the second profile's login was removed: the call must stop at the first")
			}
			if n := len(f.fake.TokenRequests()) - asked; n != 1 || f.session(t) != nil || f.fake.Replays != 0 {
				t.Fatalf("%d token requests, session present %v, replays %d: want one request, no session and no replay", n, f.session(t) != nil, f.fake.Replays)
			}
		})
	}
}

// unavailableSealer is a key store that answers nothing: a locked keychain, or an unlock dialog that nobody answers.
type unavailableSealer struct{}

func (unavailableSealer) Seal([]byte) ([]byte, error) { return nil, account.ErrKeyringUnavailable }
func (unavailableSealer) Open([]byte) ([]byte, error) { return nil, account.ErrKeyringUnavailable }

// A24(d): the exchange was answered, so the older refresh token is spent, but the key store did not take the new
// one (it stops answering once the fixture's guard exists). An older binary would present the vault's copy after
// monoes.me's 300-second reuse window and end every refresh token of the account, so the vault entry goes, nothing
// is stored, the call stops at that profile and the failure is returned.
func TestAdoptDropsTheOlderLoginWhenTheAnswerCannotBeStored(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	second := f.addProfile(t, "second-profile")
	account.SetSealerForTest(t, unavailableSealer{})
	adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil)
	if adopted || !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("adopted %v, %v: want the key store's failure", adopted, err)
	}
	if tok, _ := f.vault.Load(ctx); tok != nil {
		t.Fatal("the older login whose refresh token the exchange spent is still in the vault: an older binary would present it again")
	}
	if other, _ := second.Load(ctx); other == nil {
		t.Fatal("the second profile's login was removed: the call must stop at the first")
	}
	if f.session(t) != nil || f.fake.Replays != 0 {
		t.Fatalf("session present %v, replays %d: want no session and no replay", f.session(t) != nil, f.fake.Replays)
	}
}

// An exchange that works but whose token cannot be a session spends the older
// refresh token: the tokens monoes.me issued replace it, and the older login keeps
// working.
func TestAdoptKeepsTheOlderLoginAliveWithWhatTheExchangeIssued(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	f.fake.SetOpaqueTokens(true) // the exchange works, but its token cannot be a session
	old, _ := f.vault.Load(ctx)
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || adopted {
		t.Fatalf("opaque: adopted %v, %v", adopted, err)
	}
	kept, _ := f.vault.Load(ctx)
	if kept == nil || kept.RefreshToken == old.RefreshToken {
		t.Fatalf("the older login lost the refresh token the exchange rotated (entry present %v)", kept != nil)
	}
	c := mustClient(t, f.fake.URL, f.vault)
	if me, err := c.Me(ctx); err != nil || me.User.Username != "ada" || f.fake.Replays != 0 {
		t.Fatalf("the older login no longer works: %v (replays %d)", err, f.fake.Replays)
	}
}

// The same exchange, but the vault takes no write when the issued tokens are to replace the older ones
// (a busy database). The entry would keep the refresh token the exchange spent, which an older binary
// would present again, so it goes.
func TestAdoptDropsTheOlderLoginWhenTheVaultCannotKeepItAlive(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	f.fake.SetOpaqueTokens(true) // the exchange works, but its token cannot be a session
	if _, err := f.db.DB.Exec(`CREATE TRIGGER vault_takes_no_update BEFORE UPDATE ON vault_secrets BEGIN SELECT RAISE(ABORT, 'the vault cannot be written'); END`); err != nil {
		t.Fatal(err)
	}
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if tok, _ := f.vault.Load(ctx); tok != nil {
		t.Fatal("the vault still holds the refresh token the exchange spent: an older binary would present it again")
	}
	if f.session(t) != nil || f.fake.Refreshes != 1 || f.fake.Replays != 0 {
		t.Fatalf("session present %v, refreshes %d, replays %d: want no session, one exchange, no replay", f.session(t) != nil, f.fake.Refreshes, f.fake.Replays)
	}
}

// A dead login does not stop the call: the next profile's login is a chain of its
// own, and B5a makes one try per database.
func TestAdoptDropsADeadLoginAndTriesTheNextProfile(t *testing.T) {
	f := newAdoptFixture(t)
	ctx := context.Background()
	tok, _ := f.vault.Load(ctx)
	dead := *tok
	dead.RefreshToken = "a-refresh-token-monoes-me-never-issued"
	if err := f.vault.Save(ctx, &dead); err != nil {
		t.Fatal(err)
	}
	second := f.addProfile(t, "second-profile")
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || !adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if gone, _ := f.vault.Load(ctx); gone != nil {
		t.Fatal("the dead login is still in the vault")
	}
	if gone, _ := second.Load(ctx); gone != nil {
		t.Fatal("the adopted login is still in the vault")
	}
	if sess := f.session(t); sess == nil || sess.Host != f.fake.URL || f.fake.Replays != 0 {
		t.Fatalf("session present %v, replays %d", sess != nil, f.fake.Replays)
	}
}

// A20: the exchange spends the older refresh token, so what becomes of it in the vault is settled even
// when the caller gives up while monoes.me is answering (Ctrl-C at the first command after the update):
// the session is stored, the vault entry is gone, and nothing is presented twice.
func TestAdoptCompletesWhenTheCallerGivesUp(t *testing.T) {
	f := newAdoptFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.fake.OnToken(cancel)
	if adopted, err := library.AdoptIntoAccount(ctx, f.db.DB, f.guard, nil); err != nil || !adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if sess := f.session(t); sess == nil || sess.Host != f.fake.URL {
		t.Fatalf("session present %v", sess != nil)
	}
	if tok, _ := f.vault.Load(context.Background()); tok != nil {
		t.Fatal("the adopted login is still in the vault: the spent refresh token would be presented again")
	}
	if f.fake.Replays != 0 {
		t.Fatal("a refresh token was presented after it was spent")
	}
}

// Nothing on disk until a write: a machine with no older login makes no account folder.
func TestAdoptWritesNothingOnAMachineWithoutAnOlderLogin(t *testing.T) {
	keyring.MockInit()
	home := t.TempDir()
	testhome.SetHome(t, home)
	libraryfake.TrustKey(t)
	account.SetHostForTest(t, "http://127.0.0.1:1") // nothing listens there: any call would fail
	account.SetEnforceFromForTest(t, time.Now().Add(-time.Hour))
	db := testdb.Open(t)
	g, err := account.NewDefaultGuard()
	if err != nil {
		t.Fatal(err)
	}
	if adopted, err := library.AdoptIntoAccount(context.Background(), db.DB, g, nil); err != nil || adopted {
		t.Fatalf("adopted %v, %v", adopted, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "account")); !os.IsNotExist(err) {
		t.Fatalf("an adoption with nothing to adopt made the account folder: %v", err)
	}
}
