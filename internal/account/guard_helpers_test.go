package account_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// fakeRefresher stands in for monoes.me. It accepts one refresh token at a time
// and rotates it on every use, as the real server does, so a caller that
// refreshes twice with the same token is refused (invalid_grant) and the test
// sees a lockout, not just a call count of two.
type fakeRefresher struct {
	f     *accounttest.Fixture
	calls atomic.Int32

	mu        sync.Mutex
	valid     string        // the one refresh token the server accepts
	seq       int           // how many it has issued
	err       error         // answered instead of a token set
	empty     bool          // answer a success with no tokens in it
	badAccess string        // the access token a success carries instead of a good one
	kid       string        // the kid of the access token it mints
	delay     time.Duration // spent inside the call, so racing callers overlap
	block     bool          // wait for the context instead of answering
	hold      chan struct{} // wait until it is closed, then answer; the context still ends the wait
}

func (r *fakeRefresher) set(fn func(*fakeRefresher)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r)
}

func (r *fakeRefresher) Refresh(ctx context.Context, refreshToken string) (*account.TokenSet, error) {
	r.calls.Add(1)
	r.mu.Lock()
	block, hold, delay := r.block, r.hold, r.delay
	r.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.err != nil:
		return nil, r.err
	case r.empty:
		return &account.TokenSet{}, nil
	case refreshToken != r.valid:
		return nil, &account.RefusedError{Description: "refresh token already used"}
	}
	r.seq++
	r.valid = fmt.Sprintf("rt-%d", r.seq+1)
	access := r.f.Token(accounttest.TokenOptions{KID: r.kid})
	if r.badAccess != "" {
		access = r.badAccess
	}
	return &account.TokenSet{AccessToken: access, RefreshToken: r.valid}, nil
}

// env is one machine: a fixture (key, clock), a session directory and a fake
// monoes.me, with a guard over it. newGuard makes another guard over the same
// session, as another process would.
type env struct {
	t     *testing.T
	f     *accounttest.Fixture
	dir   string
	seal  account.Sealer
	store account.Store
	ref   *fakeRefresher
	g     *account.Guard
	bumps int
	loops []*account.Guard // every guard this env made: what settle and quiet count passes of
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := accounttest.New(t)
	dir := filepath.Join(t.TempDir(), "account")
	seal := account.NewMemorySealer()
	e := &env{t: t, f: f, dir: dir, seal: seal, store: account.OpenStore(dir, seal), ref: &fakeRefresher{f: f, valid: "rt-1"}}
	e.g = e.newGuard(0)
	return e
}

// newGuard returns a guard over the session, refreshing through the fake
// monoes.me, on the fixture clock. poll 0 is the default PollInterval.
func (e *env) newGuard(poll time.Duration) *account.Guard {
	e.t.Helper()
	g := account.NewGuard(account.GuardOptions{
		Store: account.OpenStore(e.dir, e.seal), Refresher: e.ref, Now: e.f.Clock.Now, Poll: poll,
	})
	e.t.Cleanup(g.Close)
	e.loops = append(e.loops, g)
	return g
}

// guardWith is a guard that refreshes through ref, on the fixture clock, over the
// env's session, or over store when one is given.
func (e *env) guardWith(ref account.Refresher, store ...account.Store) *account.Guard {
	e.t.Helper()
	var st account.Store = account.OpenStore(e.dir, e.seal)
	if len(store) > 0 {
		st = store[0]
	}
	g := account.NewGuard(account.GuardOptions{Store: st, Refresher: ref, Now: e.f.Clock.Now})
	e.t.Cleanup(g.Close)
	e.loops = append(e.loops, g)
	return g
}

// signIn stores a session as a login would have: a token issued age ago for
// life, hw at its iat, and the refresh token the fake server accepts.
func (e *env) signIn(age, life time.Duration) *account.Session {
	e.t.Helper()
	now := e.f.Clock.Now()
	issued := e.f.Token(accounttest.TokenOptions{IssuedAt: now.Add(-age), Lifetime: life})
	sess, err := account.NewSession(account.HostURL, issued, &account.User{ID: "user-1", Email: "u@example.test"}, now)
	if err != nil {
		e.t.Fatalf("signIn: %v", err)
	}
	sess.LastAttempt = now.Add(-age)
	e.save(sess)
	if err := e.store.SaveRefresh("rt-1"); err != nil {
		e.t.Fatalf("signIn: %v", err)
	}
	e.ref.set(func(r *fakeRefresher) { r.valid = "rt-1" })
	return sess
}

// save writes a session as another process would and makes sure its
// modification time differs from every earlier write.
func (e *env) save(sess *account.Session) {
	e.t.Helper()
	if err := e.store.Save(sess); err != nil {
		e.t.Fatalf("save: %v", err)
	}
	e.touch()
}

// touch moves the session file's modification time to a time no earlier write had.
func (e *env) touch() {
	e.t.Helper()
	e.bumps++
	at := time.Now().Add(time.Duration(e.bumps) * time.Second)
	if err := os.Chtimes(filepath.Join(e.dir, "session.json"), at, at); err != nil {
		e.t.Fatalf("touch: %v", err)
	}
}

// readReplaced reads a file a running refresher may be replacing. On Windows a
// plain open is refused with a sharing violation while the rename of a write is
// in flight (the store itself opens with delete sharing, os.ReadFile does not),
// so a failed read is tried again for a short while.
func readReplaced(path string) ([]byte, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil || time.Now().After(deadline) {
			return data, err
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (e *env) session() *account.Session {
	e.t.Helper()
	sess, err := e.store.Load()
	if err != nil || sess == nil {
		e.t.Fatalf("session: found=%v, err=%v", sess != nil, err)
	}
	return sess
}

// testWait bounds every wait for something that must happen. It returns as soon as the thing
// has happened, so the bound only matters on a machine that is too loaded to schedule the
// refresher for seconds, where a tight one fails a correct run.
const testWait = 30 * time.Second

// eventually waits for cond, polling in real time.
func eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitForFirstPass waits until g's refresher loop has finished its first pass, for a loop whose
// next tick is too far off for settle to see another.
func waitForFirstPass(t testing.TB, g *account.Guard) {
	t.Helper()
	eventually(t, "the first pass of the refresher loop", func() bool {
		n, _ := g.LoopPassesForTest(t)
		return n >= 1
	})
}

// settle gives what should NOT act, in the guards gs, time to do so, without a clock: each
// refresher loop that was started finishes three more passes (the first to finish may have begun
// before the call, the second may have read a state from before it; LoopPassesForTest counts
// them), and every OnRefused callback that was started has returned (they are started before
// Status returns, so none is on its way once none runs: CallbacksRunningForTest).
func settle(t testing.TB, gs ...*account.Guard) {
	t.Helper()
	for _, g := range gs {
		start, ended := g.LoopPassesForTest(t)
		if ended != nil {
			eventually(t, "the refresher loop to finish three more passes", func() bool {
				select {
				case <-ended:
					return true
				default:
				}
				n, _ := g.LoopPassesForTest(t)
				return n >= start+3
			})
		}
		eventually(t, "the OnRefused callbacks to return", func() bool { return g.CallbacksRunningForTest(t) == 0 })
	}
}

// transient is a failure whose outcome is known (A24): the request was never written,
// or monoes.me answered a complete 4xx that is not invalid_grant (a rate limit), so the
// refresh token was not consumed. The zero value of Settled is "unknown", which the guard answers with a
// marker, an immediate retry and, after 240 s, a dropped refresh token, so a fake
// that means a plain outage must say so; lostAnswer is the one that means the other.
func transient(reason account.Reason) error {
	return &account.TransientError{Reason: reason, Settled: true, Err: fmt.Errorf("fake network failure")}
}

// lostAnswer is a failure whose outcome is unknown: the request was written and
// nothing readable came back, so monoes.me may have rotated the refresh token.
func lostAnswer(reason account.Reason) error {
	return &account.TransientError{Reason: reason, Err: fmt.Errorf("fake connection reset after the request was written")}
}
