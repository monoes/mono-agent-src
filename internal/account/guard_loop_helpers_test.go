package account_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// loopServer is monoes.me for the refresher tests. Like the real one it accepts
// only the newest refresh token and rotates it on every use, so a refresh made
// twice shows as a refusal and a lockout, not only as a count of two. Its tokens
// live for life and carry an iat that lags the guard's clock by lag: a local
// clock that runs ahead of monoes.me. The iat is stamped when the request
// arrives, before the call is counted, so a test that moves the clock as soon as
// it sees the count cannot change the token of a call that is still in flight.
type loopServer struct {
	e     *env
	calls atomic.Int32

	mu    sync.Mutex
	valid string        // the one refresh token it accepts
	seq   int           // how many it has issued
	lag   time.Duration // the iat of what it mints is this far behind the guard's clock
	life  time.Duration // an hour when zero
	err   error         // answered instead of tokens
	takes time.Duration // the fixture clock moves by this inside each call, so a call is slow
}

func newLoopServer(e *env) *loopServer { return &loopServer{e: e, valid: "rt-1"} }

func (s *loopServer) set(fn func(*loopServer)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

// signIn stores a session as a login would have (see env.signIn) and has the
// server accept the refresh token that login stored. It writes as another process
// must, under the session lock, so that a write of the refresher that is still
// going on finishes first. The server's token and refresh.enc come first and
// session.json last: a refresher that is already polling and reads the new session
// at any instant must find what it takes to refresh it (env.signIn alone writes
// session.json before refresh.enc).
func (s *loopServer) signIn(age, life time.Duration) {
	s.e.t.Helper()
	s.e.underLock(func() {
		s.set(func(s *loopServer) { s.valid = "rt-1" })
		if err := s.e.store.SaveRefresh("rt-1"); err != nil {
			s.e.t.Fatalf("signIn: %v", err)
		}
		s.e.signIn(age, life)
	})
}

func (s *loopServer) Refresh(ctx context.Context, refreshToken string) (*account.TokenSet, error) {
	arrived := s.e.f.Clock.Now()
	s.calls.Add(1)
	s.mu.Lock()
	takes, err := s.takes, s.err
	s.mu.Unlock()
	if takes > 0 {
		s.e.f.Clock.Advance(takes)
	}
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if refreshToken != s.valid {
		return nil, &account.RefusedError{Description: "refresh token already used"}
	}
	life := s.life
	if life == 0 {
		life = time.Hour
	}
	s.seq++
	s.valid = fmt.Sprintf("rt-%d", s.seq+1)
	access := s.e.f.Token(accounttest.TokenOptions{IssuedAt: arrived.Add(-s.lag), Lifetime: life})
	return &account.TokenSet{AccessToken: access, RefreshToken: s.valid}, nil
}

// guardOn is a guard over the env's session that refreshes through ref and
// wakes every poll. It is closed when the test ends, and a Close that does not
// return within 5 seconds fails the test instead of hanging the test binary.
func (e *env) guardOn(ref account.Refresher, poll time.Duration) *account.Guard {
	e.t.Helper()
	return e.guardOnClock(ref, poll, e.f.Clock.Now)
}

// guardOnClock is guardOn on a clock of the test's own, for a test that has to
// decide what the guard's next reading of the time does.
func (e *env) guardOnClock(ref account.Refresher, poll time.Duration, now func() time.Time) *account.Guard {
	e.t.Helper()
	g := account.NewGuard(account.GuardOptions{
		Store: account.OpenStore(e.dir, e.seal), Refresher: ref, Now: now, Poll: poll,
	})
	e.loops = append(e.loops, g)
	t := e.t
	t.Cleanup(func() {
		done := make(chan struct{})
		go func() {
			defer close(done)
			g.Close()
		}()
		select {
		case <-done:
		case <-time.After(testWait):
			t.Error("Close did not return")
		}
	})
	return g
}

// refusedSession is what another process writes when monoes.me refused it.
func refusedSession() *account.Session {
	return &account.Session{V: 1, Host: account.HostURL, User: &account.User{ID: "user-1"}, State: "refused"}
}

// underLock runs fn holding the session lock, as another process must when it
// writes the session (Store: the mutating methods are safe across processes only
// under Lock). A write of the loop that is still going on, which the loop makes
// under the same lock, finishes before fn starts, and none starts in the middle
// of it; 150 ms of quiet is not a barrier against a loop that is slow to write.
func (e *env) underLock(fn func()) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	unlock, err := e.store.Lock(ctx)
	if err != nil {
		e.t.Fatalf("taking the session lock: %v", err)
	}
	defer unlock()
	fn()
}

// storeRefusal writes the refusal another process leaves when monoes.me refused it.
func (e *env) storeRefusal() {
	e.t.Helper()
	e.underLock(func() { e.save(refusedSession()) })
}

// waitForCalls waits, in real time, until the server has been called at least n times.
//
// It then waits for the pass that made the n-th call to end. The server counts a call inside the
// refresh, which the refresher makes holding the session lock and stores its answer under, so
// the lock is a barrier for that write. Without it, a test that moves the clock as soon as it
// sees the count may move it before the answer is stored, and the refresh is then recorded
// at a later time than the one the test means (a loaded machine shows it).
func waitForCalls(t *testing.T, srv *loopServer, n int32, what string) {
	t.Helper()
	eventually(t, what, func() bool { return srv.calls.Load() >= n })
	srv.e.underLock(func() {})
}

// expectCalls waits quietFor (thirty ticks of the refresher at loopPoll) and then
// fails unless the server has been called exactly n times: the refresher did
// what it should and nothing more.
func expectCalls(t *testing.T, srv *loopServer, n int32, what string) {
	t.Helper()
	quiet(t, srv.e.loops...)
	if got := srv.calls.Load(); got != n {
		t.Fatalf("%s: %d calls, want %d", what, got, n)
	}
}

// waitForAttempt waits, in real time, until session.json records an attempt at at, and then for
// the pass that wrote it to end: the refresher writes, and takes in what it wrote, under the
// session lock, which this takes once. A grant is counted when it is sent and its record is
// written after the answer, so a test that reads the session or the guard's verdict after an
// attempt waits for this, never for a while.
func waitForAttempt(t *testing.T, store account.Store, at time.Time, what string) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for {
		sess, err := store.Load()
		if err == nil && sess != nil && sess.LastAttempt.Equal(at) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: stored session = %s (%v), want the last attempt at %v", what, describe(sess), err, at)
		}
		time.Sleep(2 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	unlock, err := store.Lock(ctx)
	if err != nil {
		t.Fatalf("taking the session lock after %s: %v", what, err)
	}
	unlock()
}
