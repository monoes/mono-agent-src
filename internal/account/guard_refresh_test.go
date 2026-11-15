package account_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

func TestEnsureFreshRefreshesUnderTheMargin(t *testing.T) {
	e := newEnv(t)
	e.signIn(56*time.Minute, time.Hour) // four minutes left
	now := e.f.Clock.Now()
	st, err := e.g.EnsureFresh(context.Background())
	if err != nil || st.State != account.StateOK || !st.ValidUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("EnsureFresh = %s valid until %v, %v; want ok with a fresh hour", st.State, st.ValidUntil, err)
	}
	if e.ref.calls.Load() != 1 {
		t.Fatalf("%d network refreshes, want 1", e.ref.calls.Load())
	}
	sess := e.session()
	if sess.LastResult != "ok" || !sess.LastAttempt.Equal(now) || !sess.HW.Equal(now) || sess.State != "" || sess.User == nil || sess.User.ID != "user-1" {
		t.Fatalf("stored session = %s; want ok, attempt now, hw reset to the new iat, user kept", describe(sess))
	}
	if rt, err := e.store.LoadRefresh(); err != nil || rt != "rt-2" {
		t.Fatalf("the rotated refresh token was not stored (err %v)", err)
	}
}

func TestEnsureFreshDoesNothingWhileTheTokenIsHealthy(t *testing.T) {
	e := newEnv(t)
	e.signIn(10*time.Minute, time.Hour)
	if st, err := e.g.EnsureFresh(context.Background()); err != nil || st.State != account.StateOK || e.ref.calls.Load() != 0 {
		t.Fatalf("EnsureFresh = %s, %v with %d refreshes, want ok and none", st.State, err, e.ref.calls.Load())
	}
}

func TestTheNegativeCacheLimitsAnOfflineCallToOneAttemptAMinute(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour) // expired an hour ago
	e.ref.set(func(r *fakeRefresher) { r.err = transient(account.ReasonUnreachable) })
	ctx := context.Background()
	attempt := func() account.Status {
		st, err := e.g.EnsureFresh(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := attempt(); st.State != account.StateGrace || st.Reason != account.ReasonUnreachable {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	if sess := e.session(); sess.LastResult != "unreachable" || !sess.LastAttempt.Equal(e.f.Clock.Now()) {
		t.Fatalf("the attempt was not recorded: %s", describe(sess))
	}
	attempt()
	e.f.Clock.Advance(59 * time.Second)
	attempt()
	if e.ref.calls.Load() != 1 {
		t.Fatalf("%d attempts inside the first minute, want 1", e.ref.calls.Load())
	}
	e.f.Clock.Advance(2 * time.Second)
	attempt()
	if e.ref.calls.Load() != 2 {
		t.Fatalf("%d attempts after a minute, want 2", e.ref.calls.Load())
	}
	// Back online: the next attempt recovers.
	e.ref.set(func(r *fakeRefresher) { r.err = nil })
	e.f.Clock.Advance(time.Minute)
	if st := attempt(); st.State != account.StateOK {
		t.Fatalf("after the network came back: %s/%q", st.State, st.Reason)
	}
}

func TestEveryFailureThatIsNotInvalidGrantKeepsTheGrace(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeRefresher)
		want  account.Reason
	}{
		{"no network", func(r *fakeRefresher) { r.err = transient(account.ReasonUnreachable) }, account.ReasonUnreachable},
		{"a 5xx answer, whose outcome is unknown", func(r *fakeRefresher) { r.err = lostAnswer(account.ReasonServerError) }, account.ReasonServerError},
		{"an error of no known type", func(r *fakeRefresher) { r.err = errors.New("boom") }, account.ReasonUnreachable},
		{"a transient error with an odd reason", func(r *fakeRefresher) { r.err = transient(account.ReasonInvalid) }, account.ReasonUnreachable},
		{"a success with no tokens in it", func(r *fakeRefresher) { r.empty = true }, account.ReasonServerError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.signIn(2*time.Hour, time.Hour)
			e.ref.set(c.setup)
			st, err := e.g.EnsureFresh(context.Background())
			if err != nil || st.State != account.StateGrace || st.Reason != c.want {
				t.Fatalf("Status = %s/%q, %v, want grace/%q", st.State, st.Reason, err, c.want)
			}
			if rt, _ := e.store.LoadRefresh(); rt != "rt-1" {
				t.Fatal("a failed attempt changed the refresh token")
			}
			if e.session().State != "" {
				t.Fatal("a failed attempt marked the session")
			}
		})
	}
}

func TestInvalidGrantLocksDeletesTheRefreshTokenAndCancelsWork(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	e.ref.set(func(r *fakeRefresher) { r.err = &account.RefusedError{Description: "revoked"} })
	got := make(chan account.Status, 4)
	e.g.OnRefused(func(st account.Status) { got <- st })

	st, err := e.g.EnsureFresh(context.Background())
	if err != nil || st.State != account.StateLocked || st.Reason != account.ReasonRefused || st.Allowed() {
		t.Fatalf("EnsureFresh = %s/%q, %v, want locked/refused", st.State, st.Reason, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "refresh.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refresh.enc survived a refusal (stat err %v)", err)
	}
	sess := e.session()
	if sess.State != "refused" || sess.Reason != "revoked" || sess.AccessToken != "" || sess.User == nil || sess.User.ID != "user-1" || sess.LastResult != "refused" {
		t.Fatalf("stored session = %s; want refused, the reason, no access token, the user kept", describe(sess))
	}
	select {
	case <-got:
	case <-time.After(testWait):
		t.Fatal("OnRefused did not fire")
	}
	settle(t, e.loops...)
	if len(got) != 0 {
		t.Fatal("OnRefused fired more than once")
	}
	e.f.Clock.Advance(time.Hour)
	if st, _ := e.g.EnsureFresh(context.Background()); st.Reason != account.ReasonRefused || e.ref.calls.Load() != 1 {
		t.Fatalf("a refused session must not try again: %s/%q after %d calls", st.State, st.Reason, e.ref.calls.Load())
	}
	// Only a new sign-in unlocks it.
	e.signIn(time.Minute, time.Hour)
	e.f.Clock.Advance(account.PollInterval)
	if st := e.g.Status(); st.State != account.StateOK {
		t.Fatalf("after signing in again: %s/%q", st.State, st.Reason)
	}
}

func TestAKeyStoreThatCannotBeOpenedKeepsTheGraceAndNeverCallsTheServer(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	broken := account.NewGuard(account.GuardOptions{Store: account.OpenStore(e.dir, brokenSealer{}), Refresher: e.ref, Now: e.f.Clock.Now})
	t.Cleanup(broken.Close)
	st, err := broken.EnsureFresh(context.Background())
	if err != nil || st.State != account.StateGrace || st.Reason != account.ReasonKeyringUnavailable || e.ref.calls.Load() != 0 {
		t.Fatalf("Status = %s/%q, %v with %d calls, want grace/keyring_unavailable and none", st.State, st.Reason, err, e.ref.calls.Load())
	}
	if e.session().LastResult != "keyring_unavailable" {
		t.Fatalf("LastResult = %q", e.session().LastResult)
	}

	gone := newEnv(t)
	gone.signIn(2*time.Hour, time.Hour)
	if err := os.Remove(filepath.Join(gone.dir, "refresh.enc")); err != nil {
		t.Fatal(err)
	}
	if st, _ := gone.g.EnsureFresh(context.Background()); st.State != account.StateGrace || st.Reason != account.ReasonKeyringUnavailable || gone.ref.calls.Load() != 0 {
		t.Fatalf("a missing refresh.enc: %s/%q with %d calls", st.State, st.Reason, gone.ref.calls.Load())
	}
}

func TestATokenThatDoesNotVerifyIsNeverStoredButTheRotatedRefreshTokenIs(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(*fakeRefresher)
		wantLast string
	}{
		{"an opaque token", func(r *fakeRefresher) { r.badAccess = "opaque-0123456789" }, "server_error"},
		{"a wrong audience", func(r *fakeRefresher) {
			r.badAccess = r.f.Token(accounttest.TokenOptions{Audience: []string{"https://elsewhere.example"}})
		}, "server_error"},
		{"a key this build does not pin", func(r *fakeRefresher) { r.kid = "rotated-key" }, "key_unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			before := e.signIn(2*time.Hour, time.Hour)
			e.ref.set(c.setup)
			st, err := e.g.EnsureFresh(context.Background())
			if err != nil || st.State != account.StateGrace || st.Reason != account.ReasonServerError {
				t.Fatalf("Status = %s/%q, %v, want grace/server_error: a bad deploy must not lock anyone out", st.State, st.Reason, err)
			}
			sess := e.session()
			if sess.AccessToken != before.AccessToken || sess.LastResult != c.wantLast {
				t.Fatalf("stored session changed: token kept=%v last=%q, want the old token and %q", sess.AccessToken == before.AccessToken, sess.LastResult, c.wantLast)
			}
			if rt, _ := e.store.LoadRefresh(); rt != "rt-2" {
				t.Fatal("the server rotated the refresh token, so the new one must be kept")
			}
			if c.wantLast == "key_unknown" {
				// The session's iat is two hours back, so its grace ends 22 hours from now.
				// Until then the reason is server_error; after it, key_unknown (run update), not expired.
				start := e.f.Clock.Now()
				e.f.Clock.Set(start.Add(22*time.Hour - time.Second))
				if st := e.g.Status(); st.State != account.StateGrace || st.Reason != account.ReasonServerError {
					t.Fatalf("a second before the grace ends: %s/%q", st.State, st.Reason)
				}
				e.f.Clock.Set(start.Add(22 * time.Hour))
				if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonKeyUnknown {
					t.Fatalf("after the grace the verdict must say key_unknown (run update), got %s/%q", st.State, st.Reason)
				}
			}
		})
	}
}

func TestARolledBackClockIsRepairedByTheRefreshThatResetsHW(t *testing.T) {
	e := newEnv(t)
	sess := e.signIn(10*time.Minute, time.Hour)
	now := e.f.Clock.Now()
	// The clock was set back: hw and the last attempt lie in the future.
	sess.HW, sess.LastAttempt = now.Add(2*time.Hour), now.Add(2*time.Hour)
	e.save(sess)
	if st := e.g.Status(); st.State != account.StateLocked || st.Reason != account.ReasonClockRollback {
		t.Fatalf("Status = %s/%q, want locked/clock_rollback", st.State, st.Reason)
	}
	st, err := e.g.EnsureFresh(context.Background())
	if err != nil || st.State != account.StateOK || e.ref.calls.Load() != 1 {
		t.Fatalf("EnsureFresh = %s/%q, %v with %d calls: a stored time in the future must not hold the repair off", st.State, st.Reason, err, e.ref.calls.Load())
	}
	if hw := e.session().HW; !hw.Equal(now) {
		t.Fatalf("hw = %v, want the new token's iat %v", hw, now)
	}
}

func TestAnExpiredLoginIsRefreshedWhenMonoesMeIsBack(t *testing.T) {
	e := newEnv(t)
	e.signIn(30*time.Hour, time.Hour)
	if st := e.g.Status(); st.Reason != account.ReasonExpired {
		t.Fatalf("Status = %s/%q", st.State, st.Reason)
	}
	if st, err := e.g.EnsureFresh(context.Background()); err != nil || st.State != account.StateOK {
		t.Fatalf("EnsureFresh = %s/%q, %v, want ok: the grace ended but the refresh token still works", st.State, st.Reason, err)
	}
}

func TestDormantEnsureFreshIsANoOpAndRefreshStillWorks(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	account.SetEnforceFromForTest(t, time.Time{})
	if st, err := e.g.EnsureFresh(context.Background()); err != nil || st.State != account.StateGrace || e.ref.calls.Load() != 0 {
		t.Fatalf("EnsureFresh while dormant = %s, %v with %d calls, want no call", st.State, err, e.ref.calls.Load())
	}
	if st, err := e.g.Refresh(context.Background()); err != nil || st.State != account.StateOK || e.ref.calls.Load() != 1 {
		t.Fatalf("Refresh while dormant = %s, %v with %d calls, want one call and ok", st.State, err, e.ref.calls.Load())
	}
}

func TestAGuardWithoutARefresherNeverRefreshes(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	g := account.NewGuard(account.GuardOptions{Store: account.OpenStore(e.dir, e.seal), Now: e.f.Clock.Now})
	t.Cleanup(g.Close)
	if st, err := g.EnsureFresh(context.Background()); err != nil || st.State != account.StateGrace {
		t.Fatalf("EnsureFresh = %s, %v", st.State, err)
	}
}

func TestSeveralProcessesRefreshingAtOnceMakeOneNetworkCall(t *testing.T) {
	e := newEnv(t)
	e.signIn(56*time.Minute, time.Hour) // due for everyone
	e.ref.set(func(r *fakeRefresher) { r.delay = 40 * time.Millisecond })
	var guards []*account.Guard
	for i := 0; i < 5; i++ {
		guards = append(guards, e.newGuard(0)) // five "processes", each with its own store handle
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 15)
	for _, g := range guards {
		for j := 0; j < 3; j++ { // and three goroutines in each
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := g.EnsureFresh(context.Background()); err != nil {
					errs <- err
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("EnsureFresh: %v", err)
	}
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d network refreshes, want exactly 1 (a second one is refused: the token was rotated away)", n)
	}
	for i, g := range guards {
		if st := g.Status(); st.State != account.StateOK {
			t.Errorf("guard %d: %s/%q, want ok", i, st.State, st.Reason)
		}
	}
	if rt, _ := e.store.LoadRefresh(); rt != "rt-2" {
		t.Fatal("the stored refresh token is not the rotated one")
	}
}

func TestALockThatCannotBeTakenInTimeIsAnAdvisoryError(t *testing.T) {
	e := newEnv(t)
	e.signIn(56*time.Minute, time.Hour)
	unlock, err := account.OpenStore(e.dir, e.seal).Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	st, err := e.g.EnsureFresh(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	if st.State != account.StateOK || e.ref.calls.Load() != 0 {
		t.Fatalf("the returned Status must stay usable: %s with %d calls", st.State, e.ref.calls.Load())
	}
}

// With no session at all nothing is written before the enforcement date or while dormant; from
// the date on the first pass writes the clock-guard record (A25, guard_hwrecord_test.go).
func TestTheHighWaterMarkIsWrittenAtMostOnceAMinuteAndWithNoSessionOnlyFromTheDate(t *testing.T) {
	ctx := context.Background()
	mtime := func(e *env) time.Time {
		fi, err := os.Stat(filepath.Join(e.dir, "session.json"))
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	pin := func(e *env) {
		if err := os.Chtimes(filepath.Join(e.dir, "session.json"), old, old); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no session before the date, nothing written", func(t *testing.T) {
		e := newEnv(t)
		account.SetEnforceFromForTest(t, e.f.Clock.Now().Add(time.Hour)) // the date this test relies on: still ahead
		if _, err := e.g.EnsureFresh(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(e.dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("EnsureFresh created %s with no session before the date (stat err %v)", e.dir, err)
		}
	})
	t.Run("a stale mark is written, then not again within the minute", func(t *testing.T) {
		e := newEnv(t)
		e.signIn(10*time.Minute, time.Hour) // hw = iat, ten minutes old
		pin(e)
		now := e.f.Clock.Now()
		if _, err := e.g.EnsureFresh(ctx); err != nil {
			t.Fatal(err)
		}
		if hw := e.session().HW; !hw.Equal(now) {
			t.Fatalf("hw = %v, want now %v", hw, now)
		}
		pin(e)
		e.f.Clock.Advance(59 * time.Second)
		e.g.EnsureFresh(ctx)
		if !mtime(e).Equal(old) {
			t.Fatal("hw was written again inside the minute")
		}
		e.f.Clock.Advance(2 * time.Second)
		e.g.EnsureFresh(ctx)
		if mtime(e).Equal(old) || !e.session().HW.Equal(e.f.Clock.Now()) {
			t.Fatalf("hw was not written after the minute: %v", e.session().HW)
		}
	})
	t.Run("a mark ahead of the clock is never lowered", func(t *testing.T) {
		e := newEnv(t)
		sess := e.signIn(10*time.Minute, time.Hour)
		ahead := e.f.Clock.Now().Add(time.Minute) // inside the 5 minute allowance, so still ok
		sess.HW = ahead
		e.save(sess)
		pin(e)
		e.g.EnsureFresh(ctx)
		if !mtime(e).Equal(old) || !e.session().HW.Equal(ahead) {
			t.Fatal("hw ahead of now was rewritten")
		}
	})
	t.Run("dormant writes nothing", func(t *testing.T) {
		e := newEnv(t)
		e.signIn(10*time.Minute, time.Hour)
		pin(e)
		account.SetEnforceFromForTest(t, time.Time{})
		e.g.EnsureFresh(ctx)
		if !mtime(e).Equal(old) {
			t.Fatal("EnsureFresh wrote while dormant")
		}
	})
}

func TestACallerThatHasGivenUpStartsNoRefresh(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour) // due
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 40; i++ { // a select between "free" and "ended" picks at random: ask often
		st, err := e.g.EnsureFresh(ctx)
		if !errors.Is(err, context.Canceled) || e.ref.calls.Load() != 0 {
			t.Fatalf("EnsureFresh with an ended context: %v with %d calls, want context.Canceled and none", err, e.ref.calls.Load())
		}
		if st.State != account.StateGrace || e.session().LastResult != "ok" {
			t.Fatalf("an ended context must leave the session as it was: %s, last result %q", st.State, e.session().LastResult)
		}
	}
}

func TestRefreshFiresEveryOnRefusedCallbackEvenWhileDormant(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	e.ref.set(func(r *fakeRefresher) { r.err = &account.RefusedError{Description: "revoked"} })
	first, second := make(chan account.Status, 2), make(chan account.Status, 2)
	e.g.OnRefused(func(st account.Status) { first <- st })
	e.g.OnRefused(func(st account.Status) { second <- st }) // callbacks append: none replaces another
	account.SetEnforceFromForTest(t, time.Time{})
	st, err := e.g.Refresh(context.Background()) // the explicit refresh of `account status`, also while dormant
	if err != nil || st.Reason != account.ReasonRefused {
		t.Fatalf("Refresh = %s/%q, %v, want locked/refused", st.State, st.Reason, err)
	}
	for i, ch := range []chan account.Status{first, second} {
		select {
		case <-ch:
		case <-time.After(testWait):
			t.Fatalf("callback %d did not fire", i+1)
		}
	}
	settle(t, e.loops...)
	if len(first)+len(second) != 0 {
		t.Fatal("a callback fired more than once for one refusal")
	}
}

func TestEnsureFreshWithNoSessionLeftHasNothingToDo(t *testing.T) {
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour)
	if st := e.g.Status(); st.State != account.StateGrace { // the session is cached
		t.Fatalf("Status = %s", st.State)
	}
	if err := os.Remove(filepath.Join(e.dir, "session.json")); err != nil { // a logout
		t.Fatal(err)
	}
	if err := e.store.DeleteRefresh(); err != nil {
		t.Fatal(err)
	}
	e.f.Clock.Advance(account.PollInterval)
	st, err := e.g.EnsureFresh(context.Background())
	if err != nil || st.Reason != account.ReasonNotLoggedIn || e.ref.calls.Load() != 0 {
		t.Fatalf("EnsureFresh with no session = %s/%q, %v with %d calls, want locked/not_logged_in and no call", st.State, st.Reason, err, e.ref.calls.Load())
	}
}

// failingStore wraps a real store, records the writes a refresh makes, and
// fails the ones a test names.
type failingStore struct {
	account.Store
	mu              sync.Mutex
	writes          []string // "SaveRefresh", "Save", "DeleteRefresh", in call order
	failSaveRefresh bool
	failSave        bool
	okSaves         int // the first okSaves Saves go through although failSave is set: the marker of a grant, before the write that is meant to fail
}

func (s *failingStore) note(name string) {
	s.mu.Lock()
	s.writes = append(s.writes, name)
	s.mu.Unlock()
}

func (s *failingStore) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

func (s *failingStore) SaveRefresh(token string) error {
	s.note("SaveRefresh")
	if s.failSaveRefresh {
		return fmt.Errorf("%w: simulated", account.ErrKeyringUnavailable)
	}
	return s.Store.SaveRefresh(token)
}

func (s *failingStore) Save(sess *account.Session) error {
	s.note("Save")
	s.mu.Lock()
	fail := s.failSave
	if fail && s.okSaves > 0 {
		s.okSaves--
		fail = false
	}
	s.mu.Unlock()
	if fail {
		return errors.New("simulated: disk full")
	}
	return s.Store.Save(sess)
}

func (s *failingStore) DeleteRefresh() error {
	s.note("DeleteRefresh")
	return s.Store.DeleteRefresh()
}

// monoes.me treats a rotated-away refresh token that is presented again after its
// reuse window as theft and revokes every refresh token of the account. When the new
// one cannot be written the answer is treated as a lost one (A24, ruling d): the old
// token stays on disk with the marker that says it may be dead, so that a retry
// inside the window, which monoes.me answers again, can store it. It is not deleted:
// the marker covers it.
func TestAFailedWriteOfTheRotatedRefreshTokenKeepsTheOldOneAndTheMarker(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.signIn(2*time.Hour, time.Hour) // due; refresh.enc holds the token the server will rotate
	start := e.f.Clock.Now()
	fs := &failingStore{Store: account.OpenStore(e.dir, e.seal), failSaveRefresh: true}
	g := account.NewGuard(account.GuardOptions{Store: fs, Refresher: e.ref, Now: e.f.Clock.Now})
	t.Cleanup(g.Close)

	st, err := g.EnsureFresh(ctx)
	if !errors.Is(err, account.ErrKeyringUnavailable) || e.ref.calls.Load() != 1 {
		t.Fatalf("EnsureFresh = %v with %d calls, want the write error and one call", err, e.ref.calls.Load())
	}
	if rt, err := e.store.LoadRefresh(); err != nil || rt != "rt-1" {
		t.Fatalf("refresh.enc holds %q (%v), want the old token: the marker covers it and the retry needs it", rt, err)
	}
	if st.State != account.StateGrace || st.Reason != account.ReasonKeyringUnavailable {
		t.Fatalf("Status = %s/%q, want grace/keyring_unavailable", st.State, st.Reason)
	}
	if got := e.pendingOn(); !got.Equal(start) {
		t.Fatalf("pending_since = %v, want %v: the answer was received but not stored, which is a lost answer", got, start)
	}
	// Past the window no process presents the dead token, this one or another: the token is
	// dropped, and the server sees no second call.
	other := e.newGuard(0)
	e.f.Clock.Advance(pendingWindowPlusASecond)
	for _, guard := range []*account.Guard{g, other} {
		if st, err := guard.EnsureFresh(ctx); err != nil && !errors.Is(err, account.ErrKeyringUnavailable) || st.State != account.StateGrace || st.Reason == account.ReasonRefused {
			t.Fatalf("Status = %s/%q, %v, want grace and never refused", st.State, st.Reason, err)
		}
	}
	if n := e.ref.calls.Load(); n != 1 {
		t.Fatalf("%d calls: the dead refresh token was presented again after the window", n)
	}
	if got := e.session().LastResult; got != "unconfirmed" {
		t.Fatalf("last result = %q, want unconfirmed", got)
	}
}

func TestTheRefreshTokenIsWrittenBeforeTheSession(t *testing.T) {
	ctx := context.Background()
	t.Run("the order of the two writes", func(t *testing.T) {
		e := newEnv(t)
		e.signIn(56*time.Minute, time.Hour)
		fs := &failingStore{Store: account.OpenStore(e.dir, e.seal)}
		g := account.NewGuard(account.GuardOptions{Store: fs, Refresher: e.ref, Now: e.f.Clock.Now})
		t.Cleanup(g.Close)
		if _, err := g.EnsureFresh(ctx); err != nil {
			t.Fatal(err)
		}
		if got := fs.order(); !reflect.DeepEqual(got, []string{"Save", "SaveRefresh", "Save"}) {
			t.Fatalf("writes = %v, want the marker, then the refresh token, then the session", got)
		}
	})
	t.Run("a session that cannot be written still leaves the new refresh token usable", func(t *testing.T) {
		e := newEnv(t)
		e.signIn(2*time.Hour, time.Hour)
		fs := &failingStore{Store: account.OpenStore(e.dir, e.seal), failSave: true, okSaves: 1} // the marker is written, the session is not
		g := account.NewGuard(account.GuardOptions{Store: fs, Refresher: e.ref, Now: e.f.Clock.Now})
		t.Cleanup(g.Close)
		if _, err := g.EnsureFresh(ctx); err == nil {
			t.Fatal("the failed session write must be reported")
		}
		other := e.newGuard(0) // another process: it still sees the old session, and the new refresh token
		st, err := other.EnsureFresh(ctx)
		if err != nil || st.State != account.StateOK || e.ref.calls.Load() != 2 {
			t.Fatalf("the other process: %s/%q, %v with %d calls, want ok after a second refresh with the new token", st.State, st.Reason, err, e.ref.calls.Load())
		}
	})
}
