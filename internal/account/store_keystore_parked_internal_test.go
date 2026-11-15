package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A key store that never answers (an unlock dialog nobody sees) is asked again by
// every attempt, by a daemon every 30 s to 5 min. A call that was given up on
// cannot be cancelled: it stays parked, a goroutine and, with go-keyring on macOS,
// a `security` child process, until the key store answers. So while one call is
// parked, a new bounded call starts no other: it fails at once with
// ErrKeyringUnavailable, which the guard records as keyring_unavailable as it
// records a timeout. Each test gives its stores a limit of its own (ownLimit),
// because what one test leaves parked must not refuse the calls of the next; the
// one test of the limit that every store of a process shares cleans up after itself.

// parkedState is what these tests count: the calls the limit holds as parked, the
// goroutines of the key store calls, and the calls that reached the sealer.
func parkedState(limit *keyStoreLimit, waiting *gatedSealer) string {
	return fmt.Sprintf("%d parked, %d goroutines, %d calls in the key store", limit.parked.Load(), goroutinesIn("callKeyStore"), waiting.began.Load())
}

// A key store that never answers: attempt 1 waits for the timeout and stays
// parked; attempts 2 and 3 return at once with nothing new started, in the key
// store or as a goroutine; when the key store answers, the parked call ends, the
// count drops, and attempt 4 works.
func TestAtMostOneKeyStoreCallStaysParked(t *testing.T) {
	setKeyStoreTimeout(t, 100*time.Millisecond)
	inner := NewMemorySealer()
	_, dir := storeWithToken(t, inner, "rt-1")
	waiting := &gatedSealer{inner: inner, holdOpen: make(chan struct{}), holdSeal: make(chan struct{})}
	fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 }) // attempt 1 times out; the attempts that are refused make no timer, and attempt 4 is answered
	t.Cleanup(waiting.release)
	st := ownLimit(OpenStore(dir, waiting))
	limit := st.(*fileStore).limit

	var err error
	timed(t, "attempt 1", func() { _, err = st.LoadRefresh() })
	if !errors.Is(err, ErrKeyringUnavailable) || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("attempt 1 = %v, want the key store timeout", err)
	}
	// Control: one call is parked, and this test counts it.
	const oneParked = "1 parked, 1 goroutines, 1 calls in the key store"
	if got := parkedState(limit, waiting); got != oneParked {
		t.Fatalf("after attempt 1: %s, want %s", got, oneParked)
	}

	for _, attempt := range []struct {
		name string
		call func() error
	}{
		{"attempt 2, a SaveRefresh", func() error { return st.SaveRefresh("rt-2") }},
		{"attempt 3, a LoadRefresh", func() error { _, err := st.LoadRefresh(); return err }},
	} {
		var err error
		timed(t, attempt.name, func() { err = attempt.call() })
		if !errors.Is(err, ErrKeyringUnavailable) || !strings.Contains(err.Error(), "still waiting for an earlier request") {
			t.Fatalf("%s = %v, want ErrKeyringUnavailable saying that the key store is still waiting for an earlier request", attempt.name, err)
		}
		if got := parkedState(limit, waiting); got != oneParked {
			t.Fatalf("after %s: %s, want %s: it started another call", attempt.name, got, oneParked)
		}
	}

	waiting.release()
	if !waitUntil(func() bool { return limit.parked.Load() == 0 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("3 s after the key store answered: %s, want the parked call ended and the count back to zero", parkedState(limit, waiting))
	}
	var rt string
	timed(t, "attempt 4", func() { rt, err = st.LoadRefresh() })
	if err != nil || rt != "rt-1" {
		t.Fatalf("attempt 4 = %q, %v, want rt-1: once the parked call has returned, calls start again", rt, err)
	}
	timed(t, "attempt 5", func() { err = st.SaveRefresh("rt-2") })
	if err != nil {
		t.Fatalf("attempt 5, a SaveRefresh = %v, want no error", err)
	}
}

// Every round starts a call that times out and parks; the key store answers; the
// parked call must then end, so that the count drops and the next round's call
// starts. A call that cannot hand over a result nobody waits for would stay
// parked for good, and refuse every call after it.
func TestEveryParkedKeyStoreCallEndsAndTheNextOneStartsOnceTheKeyStoreAnswers(t *testing.T) {
	setKeyStoreTimeout(t, 20*time.Millisecond)
	inner := NewMemorySealer()
	_, dir := storeWithToken(t, inner, "rt-1")
	limit := &keyStoreLimit{}
	var current atomic.Pointer[gatedSealer] // the sealer of the round: every round makes one timer, which fires once its call is waiting
	inRound := func() bool { return current.Load().began.Load() >= 1 }
	fakeKeyStoreTimers(t, inRound, inRound, inRound, inRound, inRound, inRound)
	for round := range 6 {
		waiting := &gatedSealer{inner: inner, holdOpen: make(chan struct{}), holdSeal: make(chan struct{})}
		current.Store(waiting)
		t.Cleanup(waiting.release)
		st := OpenStore(dir, waiting)
		st.(*fileStore).limit = limit
		var err error
		if round%2 == 0 {
			timed(t, "LoadRefresh", func() { _, err = st.LoadRefresh() })
		} else {
			timed(t, "SaveRefresh", func() { err = st.SaveRefresh("rt-2") })
		}
		if !errors.Is(err, ErrKeyringUnavailable) || !strings.Contains(err.Error(), "did not answer within") {
			t.Fatalf("round %d = %v, want the key store timeout: the call has to start, so the earlier parked call has to have ended", round, err)
		}
		if got := goroutinesIn("callKeyStore"); got != 1 { // control: the abandoned call is alive, and this test sees it
			t.Fatalf("round %d: %d goroutines in the key store call while it waits, want 1", round, got)
		}
		waiting.release()
		if !waitUntil(func() bool { return limit.parked.Load() == 0 && goroutinesIn("callKeyStore") == 0 }) {
			t.Fatalf("round %d, 3 s after the key store answered: %s, want the call ended: it cannot hand over a result nobody waits for", round, parkedState(limit, waiting))
		}
	}
}

// Every store of a process shares one limit: they all ask the same key store, so
// a call that one of them left parked refuses the calls of the others. This test
// uses the limit of the process, so it leaves it as it found it, whatever happens.
func TestTwoStoresOfOneProcessShareTheLimit(t *testing.T) {
	setKeyStoreTimeout(t, 100*time.Millisecond)
	inner := NewMemorySealer()
	_, dir := storeWithToken(t, inner, "rt-1")
	waiting := &gatedSealer{inner: inner, holdOpen: make(chan struct{})}
	fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 })
	t.Cleanup(func() { waitUntil(func() bool { return processKeyStoreLimit.parked.Load() == 0 }) })
	t.Cleanup(waiting.release)
	first, second := OpenStore(dir, waiting), OpenStore(dir, inner)
	if first.(*fileStore).limit != processKeyStoreLimit || second.(*fileStore).limit != processKeyStoreLimit {
		t.Fatal("a store does not count its parked calls in the limit of the process")
	}

	var err error
	timed(t, "the first store's call", func() { _, err = first.LoadRefresh() })
	if !errors.Is(err, ErrKeyringUnavailable) || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("the first store's call = %v, want the key store timeout", err)
	}
	timed(t, "the second store's call", func() { _, err = second.LoadRefresh() })
	if !errors.Is(err, ErrKeyringUnavailable) || !strings.Contains(err.Error(), "still waiting for an earlier request") {
		t.Fatalf("the second store's call = %v, want it refused: the key store is still waiting for the first store's call", err)
	}
	waiting.release()
	if !waitUntil(func() bool { return processKeyStoreLimit.parked.Load() == 0 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("the parked call did not end after the key store answered (%d parked)", processKeyStoreLimit.parked.Load())
	}
	var rt string
	timed(t, "the second store's call", func() { rt, err = second.LoadRefresh() })
	if err != nil || rt != "rt-1" {
		t.Fatalf("the second store's call after the answer = %q, %v, want rt-1", rt, err)
	}
}

// The interactive sealer is not bounded, so it is not counted either: a call of it
// that waits for a person does not count as parked, and one that comes while a
// bounded call is parked is not refused.
func TestAnInteractiveCallIsNeitherCountedNorRefusedWhileAnotherIsParked(t *testing.T) {
	setKeyStoreTimeout(t, 100*time.Millisecond)
	inner := NewMemorySealer()
	_, dir := storeWithToken(t, inner, "rt-1")
	waiting := &gatedSealer{inner: inner, holdOpen: make(chan struct{})}
	fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 }) // the quiet call times out; the interactive call makes no timer
	t.Cleanup(waiting.release)
	quiet := ownLimit(OpenStore(dir, waiting))
	limit := quiet.(*fileStore).limit
	var err error
	timed(t, "the quiet call", func() { _, err = quiet.LoadRefresh() })
	if !errors.Is(err, ErrKeyringUnavailable) || limit.parked.Load() != 1 {
		t.Fatalf("the quiet call = %v with %d parked, want the timeout and one parked call", err, limit.parked.Load())
	}

	kek := newTestKEK(make(chan struct{}))
	t.Cleanup(kek.release)
	interactive := OpenStore(t.TempDir(), keyringSealer{kek: kek.get, mayPrompt: true})
	interactive.(*fileStore).limit = limit // the same process as the parked call
	done := make(chan error, 1)
	go func() { done <- interactive.SaveRefresh("rt-i") }()
	if !waitUntil(func() bool { return kek.asked.Load() == 1 }) {
		t.Fatal("the interactive call never reached the key store: it was refused while a bounded call was parked")
	}
	if n := limit.parked.Load(); n != 1 {
		t.Fatalf("%d parked calls while the interactive call waits for a person, want only the bounded one: the interactive call was counted", n)
	}
	kek.release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the interactive SaveRefresh = %v, want no error", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the interactive call did not return once the key store had answered")
	}
	if n := limit.parked.Load(); n != 1 {
		t.Fatalf("%d parked calls after the interactive call, want the one bounded call", n)
	}
	waiting.release()
	if !waitUntil(func() bool { return limit.parked.Load() == 0 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("the parked call did not end after the key store answered (%d parked)", limit.parked.Load())
	}
}

// A daemon that retries against a key store that never answers starts one call in
// all: every attempt records keyring_unavailable, as before, the later ones
// without waiting for the key store, and the first attempt after the key store
// answers refreshes.
func TestAGuardRetryingAgainstAKeyStoreThatNeverAnswersStartsOneCallOnly(t *testing.T) {
	r := newRig(t)
	r.signIn(2 * time.Hour) // due
	setKeyStoreTimeout(t, 100*time.Millisecond)
	waiting := &gatedSealer{inner: r.seal, holdOpen: make(chan struct{})}
	fakeKeyStoreTimers(t, func() bool { return waiting.began.Load() >= 1 }) // attempt 1 times out; attempts 2 and 3 are refused with no timer; the attempt after the answer is answered
	t.Cleanup(waiting.release)
	store := ownLimit(OpenStore(r.dir, waiting))
	limit := store.(*fileStore).limit
	g := NewGuard(GuardOptions{Store: store, Refresher: r.srv, Now: r.clock.Now})
	t.Cleanup(g.Close)

	for attempt := 1; attempt <= 3; attempt++ {
		var st Status
		var err error
		timed(t, fmt.Sprintf("attempt %d", attempt), func() { st, err = g.EnsureFresh(context.Background()) })
		if err != nil || st.State != StateGrace || st.Reason != ReasonKeyringUnavailable || r.srv.calls.Load() != 0 {
			t.Fatalf("attempt %d = %s/%q, %v with %d network refreshes, want grace/keyring_unavailable and none", attempt, st.State, st.Reason, err, r.srv.calls.Load())
		}
		if sess, err := r.store.Load(); err != nil || sess.LastResult != string(ReasonKeyringUnavailable) || !sess.LastAttempt.Equal(r.clock.Now()) {
			t.Fatalf("attempt %d: stored session = %+v (%v), want the attempt recorded as keyring_unavailable", attempt, sess, err)
		}
		if got, want := parkedState(limit, waiting), "1 parked, 1 goroutines, 1 calls in the key store"; got != want {
			t.Fatalf("after attempt %d: %s, want %s", attempt, got, want)
		}
		r.clock.Advance(time.Minute) // past the negative cache
	}

	waiting.release()
	if !waitUntil(func() bool { return limit.parked.Load() == 0 && goroutinesIn("callKeyStore") == 0 }) {
		t.Fatalf("3 s after the key store answered: %s, want the parked call ended", parkedState(limit, waiting))
	}
	st, err := g.EnsureFresh(context.Background())
	if err != nil || st.State != StateOK || r.srv.calls.Load() != 1 {
		t.Fatalf("the attempt after the answer = %s/%q, %v with %d network refreshes, want ok after one", st.State, st.Reason, err, r.srv.calls.Load())
	}
}

// The parked count must be back at zero once every call has ended, whatever the
// order in which a call's answer and the caller's timer happen. Every call here
// waits for the key store about as long as the timeout, so that the two meet and
// callKeyStore runs each branch of its compare-and-swap: the answer first, the
// timer first, and the timer firing after the call has returned. In that last
// branch the caller has raised the count for a call that is no longer running,
// and must lower it again. A count left above zero refuses every later bounded
// call of the process for good: on a machine where only the daemon refreshes, no
// refresh ever again, and locked(expired) a day later. Without the race detector
// nearly every call takes the last branch; under it, about a quarter to a half do,
// and about half time out.
func TestTheParkedCountReturnsToZeroWhenAnAnswerMeetsTheTimer(t *testing.T) {
	setKeyStoreTimeout(t, time.Millisecond)
	limit := &keyStoreLimit{}
	sealer := NewMemorySealer()
	answered, timedOut := 0, 0
	for i := range 300 {
		data, err := callKeyStore(limit, sealer, func() ([]byte, error) {
			time.Sleep(time.Millisecond) // answers as the timer fires
			return []byte("k"), nil
		})
		switch {
		case err == nil:
			answered++
			if string(data) != "k" {
				t.Fatalf("call %d returned %q with no error, want the answer that arrived as the timer fired", i, data)
			}
		case strings.Contains(err.Error(), "did not answer within"):
			timedOut++
		default:
			t.Fatalf("call %d = %v after %d answered and %d timed out: a call refused while none is parked means the count drifted up", i, err, answered, timedOut)
		}
		if !waitUntil(func() bool { return goroutinesIn("callKeyStore") == 0 }) {
			t.Fatalf("call %d: the key store call did not end", i)
		}
		if n := limit.parked.Load(); n != 0 {
			t.Fatalf("call %d: %d parked after every call has ended, want none (%d answered and %d timed out so far)", i, n, answered, timedOut)
		}
	}
}
