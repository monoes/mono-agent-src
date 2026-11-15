package account

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The key store is read (Open) and written (Seal) while the machine-wide
// session.lock is held, the write after monoes.me has rotated the refresh token,
// and the OS key stores wait without bound for a locked keychain or an unlock
// prompt nobody answers. Unbounded, one waiting key store holds the lock against
// every process of the machine, so the store bounds each call by keyStoreTimeout
// and reports an unanswered one as ErrKeyringUnavailable. These tests are in
// package account because keyStoreTimeout, the seam that shortens the wait, is
// not exported.

// gatedSealer is a Sealer whose calls wait until the test lets them go, as the
// key store of a locked keychain does while an unlock prompt waits for an answer.
// A nil gate lets that kind of call straight through to the inner sealer.
type gatedSealer struct {
	inner    Sealer
	holdOpen chan struct{}
	holdSeal chan struct{}
	once     sync.Once
	began    atomic.Int32 // calls that have entered the sealer
	ended    atomic.Int32 // calls that have left it
}

func (s *gatedSealer) Open(sealed []byte) ([]byte, error) {
	s.began.Add(1)
	defer s.ended.Add(1)
	if s.holdOpen != nil {
		<-s.holdOpen
	}
	return s.inner.Open(sealed)
}

func (s *gatedSealer) Seal(plain []byte) ([]byte, error) {
	s.began.Add(1)
	defer s.ended.Add(1)
	if s.holdSeal != nil {
		<-s.holdSeal
	}
	return s.inner.Seal(plain)
}

// release lets every held call go, once; it is safe to call twice.
func (s *gatedSealer) release() {
	s.once.Do(func() {
		if s.holdOpen != nil {
			close(s.holdOpen)
		}
		if s.holdSeal != nil {
			close(s.holdSeal)
		}
	})
}

// setKeyStoreTimeout shortens the wait for the rest of the test. A test that
// uses it must not call t.Parallel().
func setKeyStoreTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := keyStoreTimeout
	keyStoreTimeout = d
	t.Cleanup(func() { keyStoreTimeout = prev })
}

// keyStoreTimers stands in for the timer of every bounded key store call for the rest of the
// test (and, like setKeyStoreTimeout, forbids t.Parallel). The test, not the clock of the
// machine, decides when a call has waited long enough: the n-th timer a test makes fires once
// when[n-1] reports true (a predicate over the state of its gated sealer, usually that the call
// has reached it), and never when there is no predicate for it. A call that the key store answers
// is then never timed out by a runner that is slow, and one that it does not answer is timed out
// as soon as it is waiting, not after a while.
type keyStoreTimers struct {
	mu    sync.Mutex
	asked []time.Duration // the wait each timer was made for, in order
	fired int
}

func fakeKeyStoreTimers(t *testing.T, when ...func() bool) *keyStoreTimers {
	t.Helper()
	k := &keyStoreTimers{}
	prev := keyStoreTimer
	t.Cleanup(func() { keyStoreTimer = prev })
	keyStoreTimer = func(d time.Duration) (<-chan time.Time, func()) {
		k.mu.Lock()
		k.asked = append(k.asked, d)
		n := len(k.asked)
		k.mu.Unlock()
		ch := make(chan time.Time, 1)
		quit := make(chan struct{})
		if n <= len(when) && when[n-1] != nil {
			pred := when[n-1]
			go func() {
				for !pred() {
					select {
					case <-quit:
						return
					case <-time.After(time.Millisecond):
					}
				}
				k.mu.Lock()
				k.fired++
				k.mu.Unlock()
				ch <- time.Now()
			}()
		}
		var once sync.Once
		return ch, func() { once.Do(func() { close(quit) }) }
	}
	return k
}

// made is how many timers the store has asked for, and firedCount how many of them fired.
func (k *keyStoreTimers) made() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.asked)
}

func (k *keyStoreTimers) firedCount() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.fired
}

// timed runs fn on its own goroutine and fails the test unless it returns within
// 5 s: a key store that is waited for forever is the very failure these tests
// look for, and it must show as a failed assertion, not as a test that hangs. It
// returns how long fn took.
func timed(t *testing.T, what string, fn func()) time.Duration {
	t.Helper()
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(30 * time.Second):
		t.Fatalf("%s did not return within 5 s while the key store waits", what)
		return 0
	}
}

// goroutinesIn counts the goroutines whose stack mentions name.
func goroutinesIn(name string) int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, name) {
			count++
		}
	}
	return count
}

// waitUntil polls cond for up to 3 s.
func waitUntil(cond func() bool) bool {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// storeWithToken is a store holding refresh token rt, sealed by inner, and the
// directory it lives in.
func storeWithToken(t *testing.T, inner Sealer, rt string) (Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "account")
	if err := OpenStore(dir, inner).SaveRefresh(rt); err != nil {
		t.Fatal(err)
	}
	return OpenStore(dir, inner), dir
}

// ownLimit gives the store a limit of its own on the key store calls that stay
// parked, as if it were another process: what one test or one store leaves
// parked must not refuse the calls of the next.
func ownLimit(st Store) Store {
	st.(*fileStore).limit = &keyStoreLimit{}
	return st
}

func TestTheKeyStoreTimeoutIsTenSeconds(t *testing.T) {
	if keyStoreTimeout != 10*time.Second {
		t.Fatalf("keyStoreTimeout = %v, want 10 s: long enough for a keychain prompt, short enough that a refresh does not hold session.lock for a minute", keyStoreTimeout)
	}
}

func TestLoadRefreshGivesUpOnAKeyStoreThatDoesNotAnswer(t *testing.T) {
	setKeyStoreTimeout(t, 200*time.Millisecond)
	inner := NewMemorySealer()
	_, dir := storeWithToken(t, inner, "rt-1")
	waiting := &gatedSealer{inner: inner, holdOpen: make(chan struct{})}
	timers := fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 })
	t.Cleanup(waiting.release)
	st := ownLimit(OpenStore(dir, waiting))

	var token string
	var err error
	timed(t, "LoadRefresh", func() { token, err = st.LoadRefresh() })
	if token != "" || !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("LoadRefresh = %q, %v, want no token and an error that is ErrKeyringUnavailable", token, err)
	}
	if want := "did not answer within 200ms"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not say %q", err, want)
	}
	if timers.firedCount() != 1 || timers.asked[0] != keyStoreTimeout {
		t.Fatalf("LoadRefresh returned with %d timers fired, asked for %v, want it to return only when the timer for %v fired", timers.firedCount(), timers.asked, keyStoreTimeout)
	}
	// Once the key store answers, the call that was given up on ends cleanly.
	waiting.release()
	if !waitUntil(func() bool { return waiting.ended.Load() == 1 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("the abandoned call did not end after the key store answered (%d calls ended, %d goroutines left)", waiting.ended.Load(), goroutinesIn("callKeyStore"))
	}
}

func TestSaveRefreshGivesUpOnAKeyStoreThatDoesNotAnswerAndWritesNothing(t *testing.T) {
	setKeyStoreTimeout(t, 200*time.Millisecond)
	inner := NewMemorySealer()
	_, dir := storeWithToken(t, inner, "rt-1") // the token that is about to be rotated away
	waiting := &gatedSealer{inner: inner, holdSeal: make(chan struct{})}
	timers := fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 })
	t.Cleanup(waiting.release)
	st := ownLimit(OpenStore(dir, waiting))
	before := snapshotDir(t, dir)

	var err error
	timed(t, "SaveRefresh", func() { err = st.SaveRefresh("rt-2") })
	if !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("SaveRefresh = %v, want an error that is ErrKeyringUnavailable", err)
	}
	if want := "did not answer within 200ms"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not say %q", err, want)
	}
	if timers.firedCount() != 1 || timers.asked[0] != keyStoreTimeout {
		t.Fatalf("SaveRefresh returned with %d timers fired, asked for %v, want it to return only when the timer for %v fired", timers.firedCount(), timers.asked, keyStoreTimeout)
	}
	if after := snapshotDir(t, dir); after != before {
		t.Fatalf("a SaveRefresh that timed out changed the directory: %s, was %s", after, before)
	}
	// The abandoned Seal must never write: its result is dropped, and it is the
	// caller that writes refresh.enc, and only a result that arrived in time.
	waiting.release()
	if !waitUntil(func() bool { return waiting.ended.Load() == 1 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("the abandoned call did not end after the key store answered (%d calls ended, %d goroutines left)", waiting.ended.Load(), goroutinesIn("callKeyStore"))
	}
	if after := snapshotDir(t, dir); after != before {
		t.Fatalf("the Seal that was given up on wrote after the key store answered: %s, was %s", after, before)
	}
	if rt, err := OpenStore(dir, inner).LoadRefresh(); err != nil || rt != "rt-1" {
		t.Fatalf("refresh.enc holds %q (%v), want the token it held", rt, err)
	}
}

func TestSaveRefreshThatTimesOutLeavesNoFileWhereThereWasNone(t *testing.T) {
	setKeyStoreTimeout(t, 100*time.Millisecond)
	waiting := &gatedSealer{inner: NewMemorySealer(), holdSeal: make(chan struct{})}
	fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 })
	t.Cleanup(waiting.release)
	dir := filepath.Join(t.TempDir(), "account")
	st := ownLimit(OpenStore(dir, waiting))
	var err error
	timed(t, "SaveRefresh", func() { err = st.SaveRefresh("rt-1") })
	if !errors.Is(err, ErrKeyringUnavailable) {
		t.Fatalf("SaveRefresh = %v, want an error that is ErrKeyringUnavailable", err)
	}
	waiting.release()
	if !waitUntil(func() bool { return waiting.ended.Load() == 1 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatal("the abandoned Seal did not end")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a SaveRefresh that timed out created %s (stat err %v)", dir, err)
	}
}

// snapshotDir describes every file of dir by name and content, so that a test
// can show that nothing in it changed.
func snapshotDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%x", e.Name(), data))
	}
	return strings.Join(out, ";")
}

// A call that the key store answers is not slowed and not changed: it returns
// what the sealer returned, as soon as it returned it, and the sealer's own
// errors come through as they are.
func TestAKeyStoreThatAnswersIsNotSlowedOrChanged(t *testing.T) {
	inner := NewMemorySealer()
	timers := fakeKeyStoreTimers(t) // no timer ever fires: a call that waited for one would never return
	dir := filepath.Join(t.TempDir(), "account")
	st := OpenStore(dir, inner)
	var err error
	timed(t, "SaveRefresh", func() { err = st.SaveRefresh("rt-1") })
	if err != nil {
		t.Fatalf("SaveRefresh = %v, want no error with no timer fired", err)
	}
	var rt string
	timed(t, "LoadRefresh", func() { rt, err = st.LoadRefresh() })
	if err != nil || rt != "rt-1" || timers.firedCount() != 0 {
		t.Fatalf("LoadRefresh = %q, %v with %d timers fired, want rt-1 with none", rt, err, timers.firedCount())
	}

	boom := fmt.Errorf("%w: the stored key does not open the sealed refresh token", ErrKeyringUnavailable)
	refusing := OpenStore(dir, errSealer{boom})
	if _, got := refusing.LoadRefresh(); got != boom {
		t.Fatalf("LoadRefresh with a sealer that fails = %v, want the sealer's own error", got)
	}
	if got := refusing.SaveRefresh("rt-2"); got != boom {
		t.Fatalf("SaveRefresh with a sealer that fails = %v, want the sealer's own error", got)
	}
	plain := errors.New("the sealer's own error, of no known type")
	if _, got := OpenStore(dir, errSealer{plain}).LoadRefresh(); got != plain {
		t.Fatalf("LoadRefresh = %v, want an error of another type passed through unchanged", got)
	}
}

// errSealer fails every call with err.
type errSealer struct{ err error }

func (s errSealer) Seal([]byte) ([]byte, error) { return nil, s.err }
func (s errSealer) Open([]byte) ([]byte, error) { return nil, s.err }

// A refresh whose key store never answers lets go of session.lock when the
// timeout ends, so that the other processes' refreshes go through while it is
// still waiting. Without the bound one waiting key store held the lock against
// the whole machine: every other due command waited for it 25 s and recorded
// nothing.
func TestAKeyStoreThatWaitsForeverDoesNotHoldTheSessionLockPastItsTimeout(t *testing.T) {
	r := newRig(t)
	r.signIn(2 * time.Hour) // in grace: due for every process
	setKeyStoreTimeout(t, 300*time.Millisecond)
	waiting := &gatedSealer{inner: r.seal, holdOpen: make(chan struct{})}
	fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 }) // A's read of refresh.enc is the first call; B's key store answers, so none of its calls times out
	t.Cleanup(waiting.release)
	a := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, waiting)), Refresher: r.srv, Now: r.clock.Now})
	t.Cleanup(a.Close)
	b := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, r.seal)), Refresher: r.srv, Now: r.clock.Now}) // another process: its own limit
	t.Cleanup(b.Close)

	// Process A is due, takes the lock and reads refresh.enc: the key store waits.
	type answer struct {
		st  Status
		err error
	}
	answers := func(g *Guard) chan answer {
		ch := make(chan answer, 1)
		go func() {
			st, err := g.EnsureFresh(context.Background())
			ch <- answer{st, err}
		}()
		return ch
	}
	a1 := answers(a)
	if !waitUntil(func() bool { return waiting.began.Load() == 1 }) {
		t.Fatal("process A never reached the key store")
	}

	// Process B, an ordinary command a minute later (past the negative cache that
	// A's failed attempt sets), is due and waits for the lock A holds.
	r.clock.Advance(time.Minute)
	b1 := answers(b)

	// The timeout lets A go; the key store is still waiting. B then gets the lock
	// and its refresh goes through.
	select {
	case got := <-a1:
		if got.err != nil || got.st.State != StateGrace || got.st.Reason != ReasonKeyringUnavailable {
			t.Fatalf("process A = %s/%q, %v, want grace/keyring_unavailable: it had no token to send", got.st.State, got.st.Reason, got.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("process A did not return within 5 s while the key store waits")
	}
	select {
	case got := <-b1:
		if got.err != nil || got.st.State != StateOK || r.srv.calls.Load() != 1 {
			t.Fatalf("process B = %s/%q, %v with %d network refreshes, want ok after one", got.st.State, got.st.Reason, got.err, r.srv.calls.Load())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("process B did not return within 5 s: the lock was held while the key store waits")
	}
	if waiting.ended.Load() != 0 {
		t.Fatal("the key store call has ended: the lock was let go by its answer, not by the timeout")
	}
}

// The write of the new refresh token is the second key store call of a refresh,
// made after monoes.me has rotated the old one. When it times out the answer is
// treated as a lost one (A24, ruling d): the old token stays on disk with the
// marker that says it may be dead, so that a retry inside the window, which
// monoes.me answers again, can store the answer, and the attempt is recorded as a
// key store failure: the session keeps its grace.
func TestASaveRefreshThatTimesOutAfterTheGrantKeepsTheOldTokenAndTheMarkerAndRecordsTheFailure(t *testing.T) {
	r := newRig(t)
	r.signIn(2 * time.Hour) // due
	setKeyStoreTimeout(t, 300*time.Millisecond)
	waiting := &gatedSealer{inner: r.seal, holdSeal: make(chan struct{})}
	fakeKeyStoreTimers(t, nil, func() bool { return waiting.began.Load() >= 2 }) // the read of refresh.enc answers; the write is the second call and waits
	t.Cleanup(waiting.release)
	a := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, waiting)), Refresher: r.srv, Now: r.clock.Now})
	t.Cleanup(a.Close)
	start := r.clock.Now()

	var st Status
	var err error
	timed(t, "EnsureFresh", func() { st, err = a.EnsureFresh(context.Background()) })
	if r.srv.calls.Load() != 1 {
		t.Fatalf("%d network refreshes, want the one whose answer could not be stored", r.srv.calls.Load())
	}
	if !errors.Is(err, ErrKeyringUnavailable) || st.State != StateGrace || st.Reason != ReasonKeyringUnavailable {
		t.Fatalf("EnsureFresh = %s/%q, %v, want grace/keyring_unavailable and an error that is ErrKeyringUnavailable", st.State, st.Reason, err)
	}
	if rt, err := r.store.LoadRefresh(); err != nil || rt != "rt-1" {
		t.Fatalf("refresh.enc holds %q (%v), want the old token: the marker covers it and a retry inside the window needs it", rt, err)
	}
	if sess, err := r.store.Load(); err != nil || sess.LastResult != string(ReasonKeyringUnavailable) || !sess.PendingSince.Equal(start) {
		t.Fatalf("stored session = %s (%v), want the attempt recorded as keyring_unavailable and the marker of this attempt, %v", sessionFacts(sess), err, start)
	}
	// The Seal that was given up on writes nothing when the key store answers. Two
	// calls reached the key store: the Open that answered, and the Seal.
	waiting.release()
	if !waitUntil(func() bool {
		return waiting.began.Load() == 2 && waiting.ended.Load() == 2 && goroutinesIn("callKeyStore") == 0
	}) {
		t.Fatalf("the abandoned Seal did not end (%d calls began, %d ended, %d goroutines left)", waiting.began.Load(), waiting.ended.Load(), goroutinesIn("callKeyStore"))
	}
	if rt, err := r.store.LoadRefresh(); err != nil || rt != "rt-1" {
		t.Fatalf("after the Seal that was given up on ended, refresh.enc holds %q (%v), want the old token: that Seal must never write", rt, err)
	}
}

// A Seal that timed out is a lost answer (A24, ruling d), so the next process, with a key
// store that works, presents the old token again inside monoes.me's window and gets the
// same answer, which it can now store.
func TestASaveRefreshThatTimedOutIsRetriedInsideTheWindowAndTheAnswerIsStored(t *testing.T) {
	r := newRig(t)
	r.srv.window = 300 * time.Second
	r.signIn(2 * time.Hour) // due
	setKeyStoreTimeout(t, 300*time.Millisecond)
	waiting := &gatedSealer{inner: r.seal, holdSeal: make(chan struct{})}
	fakeKeyStoreTimers(t, nil, func() bool { return waiting.began.Load() >= 2 }) // the read of refresh.enc answers; the write is the second call and waits
	t.Cleanup(waiting.release)
	a := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, waiting)), Refresher: r.srv, Now: r.clock.Now})
	t.Cleanup(a.Close)
	var err error
	timed(t, "EnsureFresh", func() { _, err = a.EnsureFresh(context.Background()) })
	if !errors.Is(err, ErrKeyringUnavailable) || r.srv.calls.Load() != 1 {
		t.Fatalf("the first command = %v with %d network refreshes, want the key store error and the one grant whose answer was not stored", err, r.srv.calls.Load())
	}

	b := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, r.seal)), Refresher: r.srv, Now: r.clock.Now}) // another process: its key store works
	t.Cleanup(b.Close)
	r.clock.Advance(time.Minute)
	st, err := b.EnsureFresh(context.Background())
	if err != nil || st.State != StateOK || r.srv.calls.Load() != 2 {
		t.Fatalf("the retry = %s/%q, %v with %d network refreshes, want ok after the old token was presented again", st.State, st.Reason, err, r.srv.calls.Load())
	}
	if rt, err := r.store.LoadRefresh(); err != nil || rt != "rt-2" {
		t.Fatalf("refresh.enc holds %q (%v), want the rotated token that the repeated answer brought", rt, err)
	}
	if sess, err := r.store.Load(); err != nil || sess.LastResult != resultOK || !sess.PendingSince.IsZero() {
		t.Fatalf("stored session = %s (%v), want ok and no marker", sessionFacts(sess), err)
	}
}

// ...and after the window the old token is not presented again: it is dropped, with no
// call, and the machine signs in again.
func TestASaveRefreshThatTimedOutIsNotRetriedAfterTheWindowAndTheTokenIsDropped(t *testing.T) {
	r := newRig(t)
	r.srv.window = 300 * time.Second
	r.signIn(2 * time.Hour) // due
	setKeyStoreTimeout(t, 300*time.Millisecond)
	waiting := &gatedSealer{inner: r.seal, holdSeal: make(chan struct{})}
	fakeKeyStoreTimers(t, nil, func() bool { return waiting.began.Load() >= 2 }) // the read of refresh.enc answers; the write is the second call and waits
	t.Cleanup(waiting.release)
	a := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, waiting)), Refresher: r.srv, Now: r.clock.Now})
	t.Cleanup(a.Close)
	timed(t, "EnsureFresh", func() { _, _ = a.EnsureFresh(context.Background()) })

	b := NewGuard(GuardOptions{Store: ownLimit(OpenStore(r.dir, r.seal)), Refresher: r.srv, Now: r.clock.Now})
	t.Cleanup(b.Close)
	r.clock.Advance(pendingRetryWindow + time.Second)
	st, err := b.EnsureFresh(context.Background())
	if err != nil || st.State != StateGrace || st.Reason != ReasonUnconfirmed || r.srv.calls.Load() != 1 {
		t.Fatalf("the attempt after the window = %s/%q, %v with %d network refreshes, want grace/unconfirmed and none beyond the first", st.State, st.Reason, err, r.srv.calls.Load())
	}
	if rt, err := r.store.LoadRefresh(); err != nil || rt != "" {
		t.Fatalf("refresh.enc holds %q (%v), want the dead token gone", rt, err)
	}
	// The Seal that was given up on must not bring a token back when the key store answers at last.
	waiting.release()
	if !waitUntil(func() bool { return waiting.ended.Load() == 2 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("the abandoned Seal did not end (%d calls ended)", waiting.ended.Load())
	}
	if rt, err := r.store.LoadRefresh(); err != nil || rt != "" {
		t.Fatalf("after the Seal that was given up on ended, refresh.enc holds %q (%v), want none", rt, err)
	}
}

// A waiter notices a release soon, however long it has waited: the wait between polls
// doubles from 5 ms and stops at 50 ms. With a larger cap, or none, the polls drift apart
// (a plain doubling polls at about 5, 15, 35, 75, 155, 315, 635 and 1275 ms), and a
// holder that lets go late goes unnoticed for as long as the wait that is then running.
// A real waiter has 25 s, and would give up on a lock that had been free for seconds.
// The waits are read here instead of lived through: each one is made to pass at once,
// the holder lets go after the twelfth, and the waiter takes the lock at its next try.
func TestAWaiterPollsTheLockAtMostFiftyMillisecondsApart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "account")
	a, b := OpenStore(dir, NewMemorySealer()), OpenStore(dir, NewMemorySealer())
	unlockA, err := a.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlockA) // safe twice: a failed Lock below must not leave the lock held
	var waits []time.Duration
	prev := lockPause
	t.Cleanup(func() { lockPause = prev })
	lockPause = func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		if len(waits) == 12 {
			unlockA()
		}
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	unlockB, err := b.Lock(ctx)
	if err != nil {
		t.Fatalf("a waiter did not take the lock at its next try after the holder let go: %v", err)
	}
	unlockB()
	ms := time.Millisecond
	want := []time.Duration{5 * ms, 10 * ms, 20 * ms, 40 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms, 50 * ms}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("the waiter waited %v between its tries, want %v: doubling from 5 ms and stopping at 50 ms", waits, want)
	}
}
