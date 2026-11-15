package account

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// TokenSet is what a refresh-token grant returns. monoes.me rotates the refresh
// token on every use: RefreshToken is the token it rotated to, one equal to the
// token presented says it was not rotated, and an empty one is read as an outcome
// that is unknown (A24), so a Refresher must leave it empty when the answer named
// none, never fill in the token it presented.
type TokenSet struct{ AccessToken, RefreshToken string }

// Refresher performs the OAuth refresh-token grant with resource=Audience. It
// returns *RefusedError only when monoes.me answered invalid_grant (spec D27)
// and *TransientError for every other failure. B1b implements it; a Guard
// without one never refreshes.
//
// The guard calls Refresh with a context that is NOT cancelled when the caller
// of EnsureFresh or Refresh gives up (Ctrl-C, a signal, a deadline, the guard
// closing), and whose deadline is refreshCallTimeout (20 s) from the call. The
// server rotates the refresh token as it answers, so a request that is dropped
// after it was sent loses the new token and leaves a dead one on disk, which
// the server takes for theft the next time it is presented. A Refresher must
// therefore not rely on its caller's cancellation, and must let its request run
// to its answer or to that deadline.
//
// A *TransientError says whether the outcome of the grant is known (Settled): true
// only when the request was never written or monoes.me answered a complete 4xx that
// is not invalid_grant, so that the refresh token cannot have been consumed. Every
// 5xx, every failure after the request was written, a body cut short and a 2xx that
// cannot be used are unknown, and so is a *TransientError that does not say: the
// guard trusts the flag as it is reported, and treats the refresh token of an
// unknown outcome as one that monoes.me may have rotated: it keeps a marker
// (Session.PendingSince), presents the token again at once for pendingRetryWindow,
// while monoes.me repeats its answer, and drops it after that, so that this machine
// signs in again and the other installs of the account are not revoked (A24).
//
// Two rules the guard relies on. On any error, return a nil *TokenSet: a set
// returned beside an error is ignored, so a rotated refresh token in it is lost.
// And never return a typed-nil error, a nil *RefusedError or *TransientError
// stored in an error: the guard reads it as no error at all, so a token set that
// comes with it is a success and none is a server error, never a refusal. One
// that another error wraps is not seen through: an ordinary unreachable failure.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (*TokenSet, error)
}

// GuardOptions configures NewGuard. The guard strips the monotonic reading from
// every time Now returns and keeps and compares wall-clock time only, so a
// system clock that is set back is seen even by a long-lived process.
type GuardOptions struct {
	Store     Store            // default: the session in DefaultDir
	Refresher Refresher        // nil: never refreshes
	Now       func() time.Time // default time.Now; read with its monotonic reading stripped
	Poll      time.Duration    // default PollInterval
}

// The timings the guard owns besides the contract constants in claims.go.
const (
	hwInterval         = time.Minute                        // hw is written at most this often
	hwLockWait         = 2 * time.Second                    // a high-water write never waits longer for the lock
	refreshCallTimeout = 20 * time.Second                   // the bound on one Refresher call, whatever the caller does
	lockWaitTimeout    = refreshCallTimeout + 5*time.Second // a waiter outlasts the holder's network call, not one that is also slow in the key store
	backoffMin         = 30 * time.Second                   // the refresher's first retry
	backoffMax         = 5 * time.Minute                    // and its ceiling
	// A grant whose answer was lost is retried at once for this long after it was
	// first sent, and its refresh token is dropped after that. monoes.me answers a
	// refresh token it has rotated away with the same answer for 300 s and takes it
	// for theft after that; 240 s leaves room for the call's own duration and for
	// clocks that do not run at the same rate.
	pendingRetryWindow = 240 * time.Second
	// A reading of the clock that the refresher finds earlier than its previous one by
	// more than this, while a marker is pending, is a clock set back (A24). A smaller step
	// back, the size of an NTP correction, is ignored: it lies inside the 60 s between
	// pendingRetryWindow and monoes.me's reuse window.
	clockBackTolerance = 10 * time.Second
)

// Guard turns the stored session into a cached verdict. NewGuard does no I/O;
// the session is read on the first Status and again whenever session.json's
// modification time changes (checked at most once per Poll), so Status and
// Require stay cheap and make no network call.
type Guard struct {
	store     Store
	refresher Refresher
	now       func() time.Time
	poll      time.Duration

	sem      chan struct{} // one refresh at a time in this process
	reloadMu sync.Mutex    // one reload at a time

	mu            sync.Mutex // guards everything below
	loaded        bool       // the first read has happened
	sess          *Session   // the cached session; never modified, only replaced
	rcpt          *Receipt   // verifyToken of sess's token, so Status checks no signature
	verr          *VerifyError
	loadErr       error // the last failed read, reported as locked(invalid) while nothing is cached
	mtime         time.Time
	lastPoll      time.Time
	lastHWAttempt time.Time
	refusedNoted  bool // OnRefused has fired for the current refusal
	onRefused     []func(Status)
	closed        bool
	loopCancel    context.CancelFunc
	loopDone      chan struct{}

	callbacks atomic.Int32 // OnRefused callbacks that have started and not returned
	passes    atomic.Int64 // passes the refresher loop has finished; a test reads it to wait for the loop (LoopPassesForTest)
}

// NewGuard returns a guard over o.Store. It reads nothing and writes nothing.
func NewGuard(o GuardOptions) *Guard {
	g := &Guard{store: o.Store, refresher: o.Refresher, poll: o.Poll, sem: make(chan struct{}, 1)}
	if g.store == nil {
		g.store = OpenStore("", nil)
	}
	// Wall-clock time only. A time from time.Now carries a monotonic reading, and
	// when both operands of Before, After or Sub carry one, those compare the
	// readings alone. The monotonic clock does not follow a system clock that is
	// set back, so a guard that kept such times would not see a rollback made
	// while a long-lived process (daemon, httpapi, mcp) runs, and the high-water
	// mark stored from this clock would persist the rolled-back time over the
	// mark. Round(0) strips the reading from everything the guard reads here.
	now := o.Now
	if now == nil {
		now = time.Now
	}
	g.now = func() time.Time { return now().Round(0) }
	if g.poll <= 0 {
		g.poll = PollInterval
	}
	return g
}

// Status is the current verdict, recomputed from the cached session and the
// clock on every call.
func (g *Guard) Status() Status {
	now := g.now()
	g.pollIfDue(now)
	g.mu.Lock()
	sess, rcpt, verr, loadErr := g.sess, g.rcpt, g.verr, g.loadErr
	g.mu.Unlock()
	st := judge(sess, rcpt, verr, now)
	if sess == nil && loadErr != nil {
		st.Reason = ReasonInvalid // a session.json that cannot be read is not "not logged in"
	}
	g.note(st)
	return st
}

// Require returns nil when Status().Allowed(), else a *LoginRequiredError.
func (g *Guard) Require(ctx context.Context) error {
	if st := g.Status(); !st.Allowed() {
		return &LoginRequiredError{Status: st}
	}
	return nil
}

// OnRefused registers fn to be called once each time the verdict becomes
// locked(refused), whichever way the guard learns it: its own refresh
// (EnsureFresh, Refresh or the refresher) or a re-read of a session.json that
// another process marked refused (noticed by Status or by the refresher).
// Any number of callbacks may be registered and every one fires. A refusal
// already in effect calls fn at once; one that has ended since the guard noted
// it does not (the guard looks first). Each call runs on its own goroutine,
// after the guard's locks are released, so a slow callback blocks nothing and
// may take locks the caller of Status holds.
//
// Normally a callback is called once per refusal. In a rare race, two Status
// calls that run at once across the moment the refusal begins or ends can reach
// the guard out of order and call it twice for one refusal, so a callback must
// be idempotent. A nil fn is ignored.
func (g *Guard) OnRefused(fn func(Status)) {
	if fn == nil {
		return // it would panic on its own goroutine at the first refusal and end the process
	}
	g.mu.Lock()
	g.onRefused = append(g.onRefused, fn)
	already := g.refusedNoted
	g.mu.Unlock()
	if already {
		// The refusal the guard noted may be over in the file: look, and call fn
		// only if it is still in effect.
		if st := g.Status(); st.State == StateLocked && st.Reason == ReasonRefused {
			g.startCallback(fn, st)
		}
		return
	}
	g.Status() // a refusal in the stored session fires every callback, this one included
}

// startCallback runs fn on its own goroutine and counts it while it runs (CallbacksRunningForTest).
func (g *Guard) startCallback(fn func(Status), st Status) {
	g.callbacks.Add(1)
	go func() {
		defer g.callbacks.Add(-1)
		fn(st)
	}()
}

// note fires the OnRefused callbacks on the transition into locked(refused).
func (g *Guard) note(st Status) {
	refused := st.State == StateLocked && st.Reason == ReasonRefused
	g.mu.Lock()
	var fns []func(Status)
	if refused && !g.refusedNoted {
		fns = append(fns, g.onRefused...)
	}
	g.refusedNoted = refused
	g.mu.Unlock()
	for _, fn := range fns {
		g.startCallback(fn, st)
	}
}

// pollIfDue re-reads session.json when its modification time changed. The
// first call always reads; later calls look at the file at most once per poll.
func (g *Guard) pollIfDue(now time.Time) {
	g.mu.Lock()
	first := !g.loaded
	due := first || elapsed(now, g.lastPoll, g.poll)
	if due {
		g.lastPoll = now
	}
	g.mu.Unlock()
	if due {
		g.reload(first)
	}
}

func (g *Guard) reload(force bool) {
	g.reloadMu.Lock()
	defer g.reloadMu.Unlock()
	mt, err := g.store.Mtime()
	g.mu.Lock()
	// After a failed read the guard reads again at every poll, whatever the
	// modification time says: with nothing cached, a file that is removed since
	// has the very modification time (none) that the guard holds for it.
	unchanged := g.loaded && g.loadErr == nil && err == nil && mt.Equal(g.mtime)
	g.mu.Unlock()
	if unchanged && !force {
		return
	}
	sess, lerr := g.store.Load()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loaded = true
	if lerr != nil {
		// A read that fails on the disk (permission denied, an I/O error) says nothing
		// about the file and must not downgrade a session that worked: keep what is
		// cached and retry at the next poll. A file that is there and cannot be used is
		// what every process reads now, and a new one judges it locked(invalid): so does
		// this guard, at once, rather than keep a session that the file no longer holds
		// (a refusal written over it, say) until its grace ends. With nothing cached,
		// loadErr makes the verdict locked(invalid).
		g.loadErr = lerr
		if errors.Is(lerr, errSessionInvalid) {
			g.setSessionLocked(nil)
		}
		return
	}
	g.loadErr = nil
	g.mtime = mt
	g.setSessionLocked(sess)
}

// adopt makes sess, which the caller has just read or written under the file
// lock, the cached session. It waits for a reload in progress: a read that
// began before the caller's write must not be assigned after it.
func (g *Guard) adopt(sess *Session) {
	g.reloadMu.Lock()
	defer g.reloadMu.Unlock()
	mt, _ := g.store.Mtime()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.loaded, g.loadErr, g.mtime = true, nil, mt
	g.setSessionLocked(sess)
}

func (g *Guard) setSessionLocked(sess *Session) {
	g.sess, g.rcpt, g.verr = sess, nil, nil
	if sess != nil && sess.State != stateRefused && sess.AccessToken != "" {
		g.rcpt, g.verr = verifyToken(sess.AccessToken)
	}
}

func (g *Guard) cached() (*Session, *Receipt) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sess, g.rcpt
}

// Close stops the refresher, if one runs, and waits for it. A refresh request
// the refresher has already sent is not abandoned: the answer must be stored, so
// Close waits for it, up to refreshCallTimeout (20 s) and then the write of the
// new refresh token, which the store gives up on after keyStoreTimeout (10 s).
// It is safe to call twice, and a closed guard still answers Status and Require.
func (g *Guard) Close() {
	g.mu.Lock()
	g.closed = true
	cancel, done := g.loopCancel, g.loopDone
	g.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
