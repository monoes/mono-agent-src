package procbind

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cHandle is a worker handle that counts how often it is closed.
type cHandle struct {
	closed atomic.Int32
}

func (h *cHandle) Alive() bool        { return h.closed.Load() == 0 }
func (h *cHandle) Pid() (int, error)  { return 4242, nil }
func (h *cHandle) Close() error       { h.closed.Add(1); return nil }
func (h *cHandle) timesClosed() int32 { return h.closed.Load() }

// bclock is a clock the test moves.
type bclock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *bclock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *bclock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// regErr is Register for the many tests that only look at the error.
func regErr(b *Binder, s EntrySpec, h Handle) error {
	_, err := b.Register(s, h)
	return err
}

// genOf is the generation currently registered under a token id (0 when none).
func genOf(b *Binder, profile, id string) Generation {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.entries[entryKey{profile, id}]; e != nil {
		return e.gen
	}
	return 0
}

func newTestBinder() (*Binder, *bclock) {
	c := &bclock{t: time.Unix(2_000_000, 0)}
	return NewBinder(c.now), c
}

func spec(id string) EntrySpec {
	return EntrySpec{TokenID: id, Kind: KindSession, Profile: "p1", Scope: "session:7", Session: "conv-1", Turn: "turn-1", Expires: time.Unix(2_000_000, 0).Add(time.Hour)}
}

func activate(t *testing.T, b *Binder, s EntrySpec) *cHandle {
	t.Helper()
	h := &cHandle{}
	if err := regErr(b, s, h); err != nil {
		t.Fatal(err)
	}
	if err := b.Activate(s.Profile, s.TokenID, genOf(b, s.Profile, s.TokenID)); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestOnlyAnActiveEntryIsLookedUp(t *testing.T) {
	b, _ := newTestBinder()
	h := &cHandle{}
	s := spec("t1")
	if err := regErr(b, s, h); err != nil {
		t.Fatal(err)
	}
	if l, ok := b.Acquire("t1", "p1", "session:7"); ok {
		l.Release()
		t.Fatal("a pending entry was looked up")
	}
	if err := b.Activate("p1", "t1", genOf(b, "p1", "t1")); err != nil {
		t.Fatal(err)
	}
	l, ok := b.Acquire("t1", "p1", "session:7")
	if !ok {
		t.Fatal("an active entry was not found")
	}
	l.Release()
	for name, args := range map[string][3]string{"unknown token": {"nope", "p1", "session:7"}, "other profile": {"t1", "p2", "session:7"}, "other scope": {"t1", "p1", "session:8"}} {
		if l, ok := b.Acquire(args[0], args[1], args[2]); ok {
			l.Release()
			t.Errorf("%s was found", name)
		}
	}
}

func TestRegisterRefusals(t *testing.T) {
	b, _ := newTestBinder()
	if err := regErr(b, spec("t1"), nil); err == nil {
		t.Error("a nil handle was accepted")
	}
	bad := spec("")
	if err := regErr(b, bad, &cHandle{}); err == nil {
		t.Error("an empty token id was accepted")
	}
	noProfile := spec("t2")
	noProfile.Profile = ""
	if err := regErr(b, noProfile, &cHandle{}); err == nil {
		t.Error("an empty profile was accepted")
	}
	noScope := spec("t2b")
	noScope.Scope = ""
	if err := regErr(b, noScope, &cHandle{}); err == nil {
		t.Error("an empty scope was accepted")
	}
	h := &cHandle{}
	if err := regErr(b, spec("t3"), h); err != nil {
		t.Fatal(err)
	}
	h2 := &cHandle{}
	if err := regErr(b, spec("t3"), h2); err == nil {
		t.Error("a second registration of one token id was accepted")
	}
	if h2.timesClosed() != 0 {
		t.Error("a refused registration closed the handle (the caller still owns it)")
	}
}

func TestActivateFailsAfterRevokeOrClose(t *testing.T) {
	b, _ := newTestBinder()
	h := &cHandle{}
	if err := regErr(b, spec("t1"), h); err != nil {
		t.Fatal(err)
	}
	b.Revoke(Selector{Profile: "p1", TokenID: "t1"})
	if err := b.Activate("p1", "t1", genOf(b, "p1", "t1")); !errors.Is(err, ErrNotPending) {
		t.Errorf("activating a revoked entry: %v", err)
	}
	if h.timesClosed() != 1 {
		t.Errorf("the revoked entry's handle was closed %d times, want once", h.timesClosed())
	}
	if err := b.Activate("p1", "unknown", genOf(b, "p1", "unknown")); err == nil {
		t.Error("activating an unknown entry succeeded")
	}
	b2, _ := newTestBinder()
	h2 := &cHandle{}
	if err := regErr(b2, spec("t2"), h2); err != nil {
		t.Fatal(err)
	}
	b2.Close()
	if err := b2.Activate("p1", "t2", genOf(b2, "p1", "t2")); err == nil {
		t.Error("activating after Close succeeded")
	}
	if err := regErr(b2, spec("t3"), &cHandle{}); !errors.Is(err, ErrClosed) {
		t.Errorf("registering after Close: %v", err)
	}
	if h2.timesClosed() != 1 {
		t.Errorf("Close left the handle open or closed it twice: %d", h2.timesClosed())
	}
}

func TestRevokeSelectors(t *testing.T) {
	b, _ := newTestBinder()
	mk := func(id string, mutate func(*EntrySpec)) *cHandle {
		s := spec(id)
		mutate(&s)
		return activate(t, b, s)
	}
	a := mk("a", func(s *EntrySpec) { s.Session, s.Turn = "conv-1", "turn-1" })
	a2 := mk("a2", func(s *EntrySpec) { s.Session, s.Turn = "conv-1", "turn-2" })
	c := mk("c", func(s *EntrySpec) { s.Session, s.Turn = "conv-2", "turn-1" })
	m := mk("m", func(s *EntrySpec) { s.Kind, s.Owner, s.Session, s.Turn = KindMission, "owner/run1", "", "" })
	m2 := mk("m2", func(s *EntrySpec) { s.Kind, s.Owner, s.Session, s.Turn = KindMission, "owner", "", "" })
	m3 := mk("m3", func(s *EntrySpec) { s.Kind, s.Owner, s.Session, s.Turn = KindMission, "ownerX", "", "" })
	other := mk("o", func(s *EntrySpec) { s.Profile = "p2" })

	if got := b.Revoke(Selector{Profile: "p1", Session: "conv-1", Turn: "turn-2"}); len(got) != 1 || got[0].TokenID != "a2" {
		t.Fatalf("by session and turn: %v", got)
	}
	if a2.timesClosed() != 1 || a.timesClosed() != 0 {
		t.Fatal("only the selected entry may be released")
	}
	if got := b.Revoke(Selector{Profile: "p1", Session: "conv-1"}); len(got) != 1 || got[0].TokenID != "a" {
		t.Fatalf("by session (the rest of it): %v", got)
	}
	if got := b.Revoke(Selector{Profile: "p1", Owner: "owner"}); len(got) != 2 {
		t.Fatalf("by owner is the owner itself and its runs (owner/...) but not ownerX: %v", got)
	}
	if m.timesClosed() != 1 || m2.timesClosed() != 1 || m3.timesClosed() != 0 || c.timesClosed() != 0 {
		t.Fatal("the owner selector released the wrong entries")
	}
	if got := b.Revoke(Selector{Profile: "p1", All: true}); len(got) != 2 { // c and m3 remain in p1
		t.Fatalf("all of a profile: %v", got)
	}
	if other.timesClosed() != 0 {
		t.Fatal("another profile's entry was released by a selector for p1")
	}
	if got := b.Revoke(Selector{}); len(got) != 0 {
		t.Fatalf("an empty selector matched %v", got)
	}
	for name, sel := range map[string]Selector{"a profile alone": {Profile: "p2"}, "a profile and a kind": {Profile: "p2", Kind: KindSession}} {
		if got := b.Revoke(sel); len(got) != 0 {
			t.Fatalf("%s matched %v: a selector needs something to select by", name, got)
		}
	}
	if got := b.Revoke(Selector{Profile: "p2", TokenID: "o", Kind: KindMission}); len(got) != 0 {
		t.Fatalf("a selector of the wrong kind matched %v", got)
	}
	if got := b.Revoke(Selector{Profile: "p2", TokenID: "o", Kind: KindSession}); len(got) != 1 {
		t.Fatalf("by token id and kind: %v", got)
	}
}

func TestRevokedTokenIsATombstoneUntilItsExpiry(t *testing.T) {
	b, clk := newTestBinder()
	activate(t, b, spec("t1"))
	b.Revoke(Selector{Profile: "p1", TokenID: "t1"})
	if l, ok := b.Acquire("t1", "p1", "session:7"); ok {
		l.Release()
		t.Fatal("a revoked token was looked up")
	}
	if err := regErr(b, spec("t1"), &cHandle{}); !errors.Is(err, ErrRevoked) {
		t.Errorf("a revoked token id was registered again: %v", err)
	}
	clk.add(2 * time.Hour) // past the token's expiry
	later := spec("t1")
	later.Expires = clk.now().Add(time.Hour) // a new token that happens to reuse the id
	if err := regErr(b, later, &cHandle{}); err != nil {
		t.Errorf("after the token expired its tombstone should be gone: %v", err)
	}
	if n := b.Stats().Revoked; n != 0 {
		t.Errorf("tombstones kept after expiry: %d", n)
	}
}

func TestAnExpiredEntryIsNotAnswered(t *testing.T) {
	b, clk := newTestBinder()
	activate(t, b, spec("t1"))
	l, ok := b.Acquire("t1", "p1", "session:7")
	if !ok {
		t.Fatal("not found")
	}
	defer l.Release()
	clk.add(2 * time.Hour)
	if l.Valid() {
		t.Error("a lease on an expired entry is still valid")
	}
	if l2, ok := b.Acquire("t1", "p1", "session:7"); ok {
		l2.Release()
		t.Error("an expired entry was looked up")
	}
}

// The service's in-flight rule: a revoke after the lookup makes the lease invalid, and the handle stays open
// (borrowed by the lease) until the last lease is released, then is closed exactly once.
func TestLeaseOwnsTheHandleUntilTheLastReleaseAndSeesARevoke(t *testing.T) {
	b, _ := newTestBinder()
	h := activate(t, b, spec("t1"))
	l1, _ := b.Acquire("t1", "p1", "session:7")
	l2, _ := b.Acquire("t1", "p1", "session:7")
	if !l1.Valid() || !l2.Valid() {
		t.Fatal("fresh leases must be valid")
	}
	b.Revoke(Selector{Profile: "p1", TokenID: "t1"})
	if l1.Valid() || l2.Valid() {
		t.Fatal("a revoke after the lookup did not invalidate the leases")
	}
	if h.timesClosed() != 0 {
		t.Fatal("the handle was closed while leases still borrowed it")
	}
	l1.Release()
	l1.Release() // idempotent
	if h.timesClosed() != 0 {
		t.Fatal("the handle was closed while one lease was still out")
	}
	l2.Release()
	if h.timesClosed() != 1 {
		t.Fatalf("the handle was closed %d times after the last release, want once", h.timesClosed())
	}
	l2.Release()
	if h.timesClosed() != 1 {
		t.Fatal("a repeated release closed the handle again")
	}
	if l1.Handle() != nil || l2.Handle() != nil {
		t.Error("a released lease still exposes the handle")
	}
}

func TestReapRevokesTheEntry(t *testing.T) {
	b, _ := newTestBinder()
	h := activate(t, b, spec("t1"))
	l, _ := b.Acquire("t1", "p1", "session:7")
	b.Reap("p1", "t1", genOf(b, "p1", "t1"))
	if l.Valid() {
		t.Error("a reaped worker's lease is still valid")
	}
	l.Release()
	if h.timesClosed() != 1 {
		t.Errorf("handle closed %d times", h.timesClosed())
	}
	b.Reap("p1", "unknown", genOf(b, "p1", "unknown")) // no effect, no panic
}

func TestCloseRevokesEverythingAndKeepsLeasedHandlesUntilReleased(t *testing.T) {
	b, _ := newTestBinder()
	hp := &cHandle{}
	if err := regErr(b, spec("pending"), hp); err != nil {
		t.Fatal(err)
	}
	ha := activate(t, b, spec("active"))
	l, _ := b.Acquire("active", "p1", "session:7")
	b.Close()
	b.Close() // idempotent
	if hp.timesClosed() != 1 {
		t.Errorf("a pending handle was closed %d times by Close", hp.timesClosed())
	}
	if ha.timesClosed() != 0 || l.Valid() {
		t.Error("Close must invalidate the lease and leave its handle to the lease")
	}
	l.Release()
	if ha.timesClosed() != 1 {
		t.Errorf("active handle closed %d times", ha.timesClosed())
	}
	if l2, ok := b.Acquire("active", "p1", "session:7"); ok {
		l2.Release()
		t.Error("a closed binder looked something up")
	}
}

func TestStats(t *testing.T) {
	b, _ := newTestBinder()
	if err := regErr(b, spec("p"), &cHandle{}); err != nil {
		t.Fatal(err)
	}
	activate(t, b, spec("a"))
	activate(t, b, spec("r"))
	b.Revoke(Selector{Profile: "p1", TokenID: "r"})
	if st := b.Stats(); st.Pending != 1 || st.Active != 1 || st.Revoked != 1 {
		t.Errorf("stats = %+v", st)
	}
}

// Leases are held across a concurrent revoke: once Revoke has returned, no Valid() may be true, and every handle is
// closed exactly once in the end.
func TestNoLeaseIsValidAfterARevokeReturned(t *testing.T) {
	b, _ := newTestBinder()
	const n = 32
	handles := make([]*cHandle, n)
	for i := range handles {
		s := spec("t" + string(rune('A'+i)))
		handles[i] = activate(t, b, s)
	}
	var revoked atomic.Bool
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		id := "t" + string(rune('A'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, ok := b.Acquire(id, "p1", "session:7")
			if !ok {
				return
			}
			defer l.Release()
			for j := 0; j < 2000; j++ {
				after := revoked.Load() // read BEFORE asking: a true here means the revoke had already returned
				if v := l.Valid(); v && after {
					t.Errorf("%s: a lease was valid after Revoke had returned", id)
					return
				}
			}
		}()
	}
	close(start)
	b.Revoke(Selector{Profile: "p1", All: true})
	revoked.Store(true)
	wg.Wait()
	b.Close()
	for i, h := range handles {
		if got := h.timesClosed(); got != 1 {
			t.Errorf("handle %d closed %d times", i, got)
		}
	}
}

// Register, activate, look up, revoke and release at once, with the race detector: every handle ends up closed
// exactly once.
func TestConcurrentUseClosesEveryHandleOnce(t *testing.T) {
	b, _ := newTestBinder()
	const n = 64
	handles := make([]*cHandle, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		handles[i] = &cHandle{}
		id := "t" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		s := spec(id)
		wg.Add(1)
		go func(h *cHandle) {
			defer wg.Done()
			if err := regErr(b, s, h); err != nil {
				t.Error(err)
				return
			}
			_ = b.Activate(s.Profile, s.TokenID, genOf(b, s.Profile, s.TokenID))
			for j := 0; j < 20; j++ {
				if l, ok := b.Acquire(s.TokenID, "p1", "session:7"); ok {
					_ = l.Valid()
					l.Release()
				}
			}
		}(handles[i])
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Revoke(Selector{Profile: "p1", TokenID: s.TokenID})
		}()
	}
	wg.Wait()
	b.Close()
	for i, h := range handles {
		if got := h.timesClosed(); got != 1 {
			t.Errorf("handle %d closed %d times", i, got)
		}
	}
}

// An entry that expires without ever being revoked, active or never activated, releases its handle and its id.
func TestExpiredEntriesReleaseTheirHandleAndTheirId(t *testing.T) {
	b, clk := newTestBinder()
	hActive := activate(t, b, spec("active"))
	hPending := &cHandle{}
	if err := regErr(b, spec("pending"), hPending); err != nil {
		t.Fatal(err)
	}
	clk.add(2 * time.Hour)
	if err := b.Activate("p1", "pending", genOf(b, "p1", "pending")); !errors.Is(err, ErrNotPending) {
		t.Errorf("activating an expired pending entry: %v", err)
	}
	if st := b.Stats(); st.Pending != 0 || st.Active != 0 {
		t.Errorf("expired entries are still counted live: %+v", st)
	}
	if hActive.timesClosed() != 1 || hPending.timesClosed() != 1 {
		t.Errorf("expired handles closed %d and %d times, want once each", hActive.timesClosed(), hPending.timesClosed())
	}
	reused := spec("active")
	reused.Expires = clk.now().Add(time.Hour)
	if err := regErr(b, reused, &cHandle{}); err != nil {
		t.Errorf("the id of an expired token could not be registered again: %v", err)
	}
}

func TestTheExpiryBoundaryIsExclusive(t *testing.T) {
	b, clk := newTestBinder()
	s := spec("t1")
	activate(t, b, s)
	l, _ := b.Acquire("t1", "p1", "session:7")
	defer l.Release()
	clk.add(time.Hour - time.Nanosecond)
	if !l.Valid() {
		t.Fatal("the lease was invalid just before the expiry")
	}
	clk.add(time.Nanosecond) // now == Expires
	if l.Valid() {
		t.Error("the lease was valid at the expiry instant")
	}
	if l2, ok := b.Acquire("t1", "p1", "session:7"); ok {
		l2.Release()
		t.Error("an entry was looked up at its expiry instant")
	}
}

// A lease still out when the token expires keeps the handle open until it is released, and the entry is not
// forgotten before that.
func TestAnExpiredEntryWithALeaseKeepsItsHandleUntilRelease(t *testing.T) {
	b, clk := newTestBinder()
	h := activate(t, b, spec("t1"))
	l, _ := b.Acquire("t1", "p1", "session:7")
	clk.add(2 * time.Hour)
	b.Stats() // prunes
	if h.timesClosed() != 0 {
		t.Fatal("the handle was closed under a lease that was still out")
	}
	if l.Valid() {
		t.Error("a lease on an expired entry is valid")
	}
	l.Release()
	if h.timesClosed() != 1 {
		t.Errorf("handle closed %d times after the release", h.timesClosed())
	}
}

func TestTokenIdsAreUniqueWithinAProfileOnly(t *testing.T) {
	b, _ := newTestBinder()
	activate(t, b, spec("same"))
	s2 := spec("same")
	s2.Profile = "p2"
	h2 := activate(t, b, s2)
	if got := b.Revoke(Selector{Profile: "p1", TokenID: "same"}); len(got) != 1 {
		t.Fatalf("revoking p1's token: %v", got)
	}
	if h2.timesClosed() != 0 {
		t.Error("revoking p1's token released p2's entry that has the same id")
	}
	if got := b.Revoke(Selector{Profile: "p1", TokenID: "same"}); len(got) != 0 {
		t.Errorf("revoked again: %v", got)
	}
	if l, ok := b.Acquire("same", "p2", "session:7"); !ok {
		t.Error("p2's entry stopped answering")
	} else {
		l.Release()
	}
}

// A handle's Close may call back into the Binder: it is not closed under the registry lock.
type callbackHandle struct {
	cHandle
	b *Binder
}

func (h *callbackHandle) Close() error {
	h.b.Stats() // would deadlock if Close ran under the lock
	return h.cHandle.Close()
}

func TestHandlesAreClosedOutsideTheLock(t *testing.T) {
	b, _ := newTestBinder()
	h := &callbackHandle{b: b}
	s := spec("t1")
	if err := regErr(b, s, h); err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t1", genOf(b, "p1", "t1")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Revoke(Selector{Profile: "p1", TokenID: "t1"})
		l := (*Lease)(nil)
		l.Release()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Revoke deadlocked: the handle was closed under the registry lock")
	}
	if h.timesClosed() != 1 {
		t.Errorf("closed %d times", h.timesClosed())
	}
}

func TestANilLeaseIsSafe(t *testing.T) {
	var l *Lease
	if l.Valid() || l.Handle() != nil {
		t.Error("a nil lease is valid")
	}
	l.Release()
}

// A selector for one profile's token never reaches another profile's token.
func TestATokenIdSelectorStaysInItsProfile(t *testing.T) {
	b, _ := newTestBinder()
	s := spec("t1")
	s.Profile = "p2"
	h := activate(t, b, s)
	if got := b.Revoke(Selector{Profile: "p1", TokenID: "t1"}); len(got) != 0 || h.timesClosed() != 0 {
		t.Errorf("a p1 selector reached a p2 token: %v", got)
	}
}

// timers is a fake timer: the test fires what the binder scheduled.
type timers struct {
	mu  sync.Mutex
	fns []func()
	// late makes stop report that the timer had already fired: the callback stays queued and runs on fireAll,
	// as a timer that fires just before it is stopped does.
	late bool
}

func (ts *timers) after(d time.Duration, f func()) func() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	i := len(ts.fns)
	ts.fns = append(ts.fns, f)
	return func() bool {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		if ts.late {
			return false
		}
		had := ts.fns[i] != nil
		ts.fns[i] = nil
		return had
	}
}

func (ts *timers) fireAll() {
	ts.mu.Lock()
	fns := append([]func(){}, ts.fns...)
	ts.mu.Unlock()
	for _, f := range fns {
		if f != nil {
			f()
		}
	}
}

// The timer closes the handle of a token that expired while the binder was idle: nothing else calls it.
func TestAnIdleExpiredEntryReleasesItsHandleByItsTimer(t *testing.T) {
	clk := &bclock{t: time.Unix(2_000_000, 0)}
	ts := &timers{}
	b := NewBinderWithTimer(clk.now, ts.after)
	h := activate(t, b, spec("t1"))
	ts.fireAll() // not expired yet: the timer must not end a live entry
	if h.timesClosed() != 0 {
		t.Fatal("the timer closed the handle of a live entry")
	}
	clk.add(2 * time.Hour)
	ts.fireAll()
	if h.timesClosed() != 1 {
		t.Fatalf("an idle binder kept the handle of an expired token open (closed %d times)", h.timesClosed())
	}
	if l, ok := b.Acquire("t1", "p1", "session:7"); ok {
		l.Release()
		t.Error("an expired entry was looked up")
	}
}

// A lease out at expiry keeps the handle until its release, and the release closes it with nothing else running.
func TestReleasingALeasePastTheExpiryClosesTheHandle(t *testing.T) {
	clk := &bclock{t: time.Unix(2_000_000, 0)}
	ts := &timers{}
	b := NewBinderWithTimer(clk.now, ts.after)
	h := activate(t, b, spec("t1"))
	l, _ := b.Acquire("t1", "p1", "session:7")
	clk.add(2 * time.Hour)
	if h.timesClosed() != 0 {
		t.Fatal("the handle was closed under a lease")
	}
	l.Release() // no Valid() call, no timer fired, nothing else touched the binder: Release alone must end it
	if h.timesClosed() != 1 {
		t.Fatalf("the handle of an expired token stayed open after the last lease (closed %d times)", h.timesClosed())
	}
}

func TestRegisteringAnAlreadyExpiredTokenIsRefused(t *testing.T) {
	b, clk := newTestBinder()
	s := spec("t1")
	clk.add(2 * time.Hour)
	if _, err := b.Register(s, &cHandle{}); !errors.Is(err, ErrExpired) {
		t.Errorf("registering an expired token: %v", err)
	}
}

// A token id may be registered again once the old token expired; a late notification about the OLD worker
// (its reap, a late activate, a revoke aimed at its registration) must not touch the new entry.
func TestADelayedOperationOnAnOldRegistrationDoesNotTouchTheNewOne(t *testing.T) {
	b, clk := newTestBinder()
	oldH := &cHandle{}
	oldGen, err := b.Register(spec("t1"), oldH)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t1", oldGen); err != nil {
		t.Fatal(err)
	}
	clk.add(2 * time.Hour) // the old token expired
	fresh := spec("t1")
	fresh.Expires = clk.now().Add(time.Hour)
	newH := &cHandle{}
	newGen, err := b.Register(fresh, newH)
	if err != nil {
		t.Fatalf("the id of an expired token could not be registered again: %v", err)
	}
	if newGen == oldGen || oldH.timesClosed() != 1 {
		t.Fatalf("generations %d and %d, old handle closed %d times", oldGen, newGen, oldH.timesClosed())
	}
	b.Reap("p1", "t1", oldGen) // the old worker's exit, delivered late
	if err := b.Activate("p1", "t1", oldGen); !errors.Is(err, ErrStale) {
		t.Errorf("a late activate of the old registration: %v", err)
	}
	if got := b.Revoke(Selector{Profile: "p1", TokenID: "t1", Generation: oldGen}); len(got) != 0 {
		t.Errorf("a revoke aimed at the old registration reached %v", got)
	}
	if newH.timesClosed() != 0 {
		t.Fatal("a stale notification closed the new worker's handle")
	}
	if err := b.Activate("p1", "t1", newGen); err != nil {
		t.Fatalf("the new registration could not be activated after the stale operations: %v", err)
	}
	if l, ok := b.Acquire("t1", "p1", "session:7"); !ok {
		t.Error("the new entry stopped answering")
	} else {
		l.Release()
	}
	// the right generation does work
	if got := b.Revoke(Selector{Profile: "p1", TokenID: "t1", Generation: newGen}); len(got) != 1 {
		t.Errorf("a revoke of the current registration: %v", got)
	}
	// generation 0 is not a wildcard for the single-entry operations
	b.Reap("p1", "t1", 0)
	if err := b.Activate("p1", "t1", 0); err == nil {
		t.Error("activating with generation 0 succeeded")
	}
}

// A timer of the OLD registration that fires after the id was registered again must not touch the new entry.
func TestAStaleExpiryTimerDoesNotTouchTheNewRegistration(t *testing.T) {
	clk := &bclock{t: time.Unix(2_000_000, 0)}
	ts := &timers{late: true}
	b := NewBinderWithTimer(clk.now, ts.after)
	oldGen, err := b.Register(spec("t1"), &cHandle{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t1", oldGen); err != nil {
		t.Fatal(err)
	}
	clk.add(2 * time.Hour)
	fresh := spec("t1")
	fresh.Expires = clk.now().Add(time.Hour)
	newH := &cHandle{}
	newGen, err := b.Register(fresh, newH) // prunes the expired one; its timer is queued and fires late
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Activate("p1", "t1", newGen); err != nil {
		t.Fatal(err)
	}
	ts.fireAll() // the old registration's timer, delivered late
	if newH.timesClosed() != 0 {
		t.Fatal("a stale timer closed the new worker's handle")
	}
	if l, ok := b.Acquire("t1", "p1", "session:7"); !ok {
		t.Error("a stale timer removed the new entry")
	} else {
		l.Release()
	}
}
