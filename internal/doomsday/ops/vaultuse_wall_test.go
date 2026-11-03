package ops

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// jump moves the clock without waking anyone, so the timeout signal does not fire and the arrival rule alone decides.
func (c *fakeClock) jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// noTimerClock never fires the wait for the call's deadline, so only the arrival rule can produce a timeout. Only
// the wait started BEFORE the clock reached the deadline is held back (the timer goroutine's); the slot-end wait
// Run makes afterwards finds the deadline already passed and goes through. entered is closed once the timer is held,
// and the transport waits for it before moving the clock, so the outcome does not depend on goroutine scheduling.
type noTimerClock struct {
	*fakeClock
	deadline time.Time
	entered  chan struct{}
	once     *sync.Once
}

func (c noTimerClock) Wait(ctx context.Context, until time.Time) error {
	if until.Equal(c.deadline) && c.fakeClock.Now().Before(until) {
		c.once.Do(func() { close(c.entered) })
		<-ctx.Done()
		return ctx.Err()
	}
	return c.fakeClock.Wait(ctx, until)
}

func TestABodyThatCompletesAtMaxCallIsATimeoutEvenWithoutTheTimerSignal(t *testing.T) { // T5f10
	var clk *fakeClock
	entered := make(chan struct{})
	tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			return Response{}, errors.New("the timer wait never started")
		}
		clk.jump(1000 * time.Millisecond)
		return Response{Status: 200, Body: io.NopCloser(strings.NewReader("abc"))}, nil
	}}
	r := newVRig(t, tr)
	clk = r.clk
	r.x.Clock = noTimerClock{r.clk, r.at(1000 * time.Millisecond), entered, new(sync.Once)}
	ch := make(chan result, 1)
	go func() {
		e, err := r.x.Run(context.Background(), approvedVault(testVault), testReq)
		ch <- result{e, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
			t.Fatalf("%+v %v", res.env, res.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// deadClock never signals at all.
type deadClock struct{ now time.Time }

func (c deadClock) Now() time.Time                              { return c.now }
func (c deadClock) Wait(ctx context.Context, _ time.Time) error { <-ctx.Done(); return ctx.Err() }

// closeTracker is a body that blocks in Read, like a child that never writes, until it is closed.
type closeTracker struct {
	closed chan struct{}
	once   sync.Once
}

func (b *closeTracker) Read([]byte) (int, error) { <-b.closed; return 0, io.ErrClosedPipe }
func (b *closeTracker) Close() error             { b.once.Do(func() { close(b.closed) }); return nil }

// The production bound: a body that never delivers and a clock that never signals still end at MaxCall (plus the
// real-time grace) with the timeout outcome, and the body is closed (the child is killed).
func TestARealWallClockBoundsACallWhoseClockAndBodyNeverSignal(t *testing.T) {
	body := &closeTracker{closed: make(chan struct{})}
	tr := fakeTransport{func(ctx context.Context, _ VaultArgs, _ []byte) (Response, error) {
		return Response{Status: 200, Body: body}, nil
	}}
	v := *testVault
	v.MaxCallMS, v.SlotMS = 50, 10
	x := &VaultExecutor{Clock: deadClock{time.Unix(1_000_000, 0)}, Transport: tr, Secrets: fakeSecrets{"s"}, WallGrace: 50 * time.Millisecond}
	ch := make(chan result, 1)
	start := time.Now()
	go func() { e, err := x.Run(context.Background(), approvedVault(&v), testReq); ch <- result{e, err} }()
	select {
	case res := <-ch:
		if res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
			t.Fatalf("%+v %v", res.env, res.err)
		}
		if d := time.Since(start); d < 90*time.Millisecond {
			t.Fatalf("returned after %v, before MaxCall plus grace", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run outlived its deadline")
	}
	select {
	case <-body.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the body (the child) was not closed")
	}
}

// With a working real clock the timeout comes at MaxCall itself, and the stuck reader is released.
func TestARealTimerEndsACallWhoseBodyNeverWrites(t *testing.T) {
	body := &closeTracker{closed: make(chan struct{})}
	tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: body}, nil
	}}
	v := *testVault
	v.MaxCallMS, v.SlotMS = 50, 10
	x := &VaultExecutor{Clock: realClock{}, Transport: tr, Secrets: fakeSecrets{"s"}}
	ch := make(chan result, 1)
	go func() { e, err := x.Run(context.Background(), approvedVault(&v), testReq); ch <- result{e, err} }()
	select {
	case res := <-ch:
		if res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
			t.Fatalf("%+v %v", res.env, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run outlived its deadline")
	}
	select {
	case <-body.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the body was not closed")
	}
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Wait(ctx context.Context, until time.Time) error {
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runAsync runs the executor in a goroutine so a test never hangs on a regression.
func runAsync(x *VaultExecutor, v *VaultUse) <-chan result {
	ch := make(chan result, 1)
	go func() { e, err := x.Run(context.Background(), approvedVault(v), testReq); ch <- result{e, err} }()
	return ch
}

func mustReturn(t *testing.T, ch <-chan result, within time.Duration) result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(within):
		t.Fatal("Run did not return")
		return result{}
	}
}

func shortVault(maxCall, slot int64) *VaultUse {
	v := *testVault
	v.MaxCallMS, v.SlotMS = maxCall, slot
	return &v
}

func stuckTransport(body io.ReadCloser) Transport {
	return fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: body}, nil
	}}
}

// The wall waits the grace (a wall timer without it fires at MaxCall) and the whole of Run stays inside ONE real-time
// budget, MaxCall + slot + grace, not two sequential windows (a frozen clock made it twice that).
func TestTheWallBudgetIsOneWindowAndIncludesTheGrace(t *testing.T) {
	body := &closeTracker{closed: make(chan struct{})}
	x := &VaultExecutor{Clock: deadClock{time.Unix(1_000_000, 0)}, Transport: stuckTransport(body), Secrets: fakeSecrets{"s"}, WallGrace: 400 * time.Millisecond}
	start := time.Now()
	early := time.AfterFunc(250*time.Millisecond, func() { // the exchange is not forced before MaxCall + grace
		select {
		case <-body.closed:
			t.Error("the body was closed before MaxCall plus the grace")
		default:
		}
	})
	defer early.Stop()
	res := mustReturn(t, runAsync(x, shortVault(50, 10)), 10*time.Second)
	if d := time.Since(start); res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) || d < 440*time.Millisecond || d > 700*time.Millisecond {
		t.Fatalf("%+v %v after %v, want the timeout after about 460 ms", res.env, res.err, d)
	}
}

// heldClock holds back every wait until release is called, then behaves as the fake clock, so only the wall can end
// the exchange.
type heldClock struct {
	*fakeClock
	held atomic.Bool
}

func (c *heldClock) Wait(ctx context.Context, until time.Time) error {
	for c.held.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
	return c.fakeClock.Wait(ctx, until)
}

// On the wall path the arrival is MaxCall, so the release is the END of the last slot on the injected clock, not
// the first slot's.
func TestTheWallPathReleasesAtTheLastSlotOfTheInjectedClock(t *testing.T) {
	r := newVRig(t, stuckTransport(&closeTracker{closed: make(chan struct{})}))
	// the first wall (the exchange's) fires at once, the second (the slot end's) never: no real-time dependence
	var walls atomic.Int32
	r.x.newWall = func(time.Duration) (<-chan time.Time, func()) {
		c := make(chan time.Time, 1)
		if walls.Add(1) == 1 {
			c <- time.Now()
		}
		return c, func() {}
	}
	hc := &heldClock{fakeClock: r.clk}
	hc.held.Store(true)
	r.x.Clock = hc
	ch := runAsync(r.x, shortVault(50, 10))
	for i := 0; walls.Load() < 2 && i < 5000; i++ { // Run has taken its start instant and reached the slot-end wait
		time.Sleep(time.Millisecond)
	}
	hc.held.Store(false)
	r.clk.Advance(30 * time.Millisecond) // past the first slot, short of the last
	select {
	case res := <-ch:
		t.Fatalf("released before the last slot ended: %+v", res)
	case <-time.After(100 * time.Millisecond):
	}
	r.clk.Advance(30 * time.Millisecond)
	if res := mustReturn(t, ch, 5*time.Second); res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

// Documented behaviour: when the injected clock does not reach the slot end in the wall budget, the envelope is
// released at the wall, BEFORE the injected slot end. The charter's maximum duration wins over the quantisation.
func TestTheWallReleasesBeforeTheInjectedSlotEndWhenTheClockIsStuck(t *testing.T) {
	tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: io.NopCloser(&slowReader{})}, nil
	}}
	r := newVRig(t, tr)
	r.x.WallGrace = 30 * time.Millisecond
	res := mustReturn(t, runAsync(r.x, shortVault(50, 10)), 5*time.Second) // the fake clock never advances
	if res.err != nil || res.env.Outcome != OutcomeOK {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

type slowReader struct{ done bool }

func (s *slowReader) Read(p []byte) (int, error) {
	if s.done {
		return 0, io.EOF
	}
	s.done = true
	return copy(p, "abc"), nil
}

type errClock struct{ *fakeClock }

func (errClock) Wait(context.Context, time.Time) error { return errors.New("clock failure") }

// Any Wait error other than the wall's own expiry is a cancel: nothing is delivered, no early release.
func TestAClockErrorAtTheSlotEndIsACancelNotAnEarlyRelease(t *testing.T) {
	tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: io.NopCloser(&slowReader{})}, nil
	}}
	r := newVRig(t, tr)
	r.x.Clock = errClock{r.clk}
	res := mustReturn(t, runAsync(r.x, testVault), 5*time.Second)
	if !errors.Is(res.err, ErrCancelled) || res.env != (Envelope{}) {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

// A clock whose Wait ignores ctx cannot hold Run past the wall.
func TestAClockThatIgnoresItsContextCannotHoldRun(t *testing.T) {
	stop := make(chan struct{})
	defer close(stop)
	x := &VaultExecutor{Clock: deafClock{stop: stop}, Transport: stuckTransport(&closeTracker{closed: make(chan struct{})}), Secrets: fakeSecrets{"s"}, WallGrace: 30 * time.Millisecond}
	res := mustReturn(t, runAsync(x, shortVault(50, 10)), 5*time.Second)
	if res.err != nil || res.env != (Envelope{Outcome: OutcomeTimeout}) {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}

type deafClock struct{ stop chan struct{} }

func (deafClock) Now() time.Time { return time.Unix(1_000_000, 0) }
func (c deafClock) Wait(context.Context, time.Time) error {
	<-c.stop
	return errors.New("stopped")
}

// Both real-time timers are stopped on every path.
func TestTheWallTimersAreStopped(t *testing.T) {
	var made, stopped atomic.Int32
	r := newVRig(t, fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) {
		return Response{Status: 200, Body: io.NopCloser(&slowReader{})}, nil
	}})
	r.x.newWall = func(d time.Duration) (<-chan time.Time, func()) {
		made.Add(1)
		return time.NewTimer(time.Minute).C, func() { stopped.Add(1) }
	}
	ch := r.start(testVault, context.Background())
	r.finish(ch, 100*time.Millisecond)
	if made.Load() != 2 || stopped.Load() != 2 {
		t.Fatalf("%d timers made, %d stopped", made.Load(), stopped.Load())
	}
}

// countingBody blocks in Read until closed and counts the closes.
type countingBody struct {
	closed  chan struct{}
	once    sync.Once
	closes  atomic.Int32
	readEnd chan struct{}
}

func (b *countingBody) Read([]byte) (int, error) {
	<-b.closed
	defer close(b.readEnd)
	return 0, io.ErrClosedPipe
}
func (b *countingBody) Close() error {
	b.closes.Add(1)
	b.once.Do(func() { close(b.closed) })
	return nil
}

// The body is closed exactly once even though two paths (cancel and the end of the exchange) want it closed.
func TestTheBodyIsClosedExactlyOnce(t *testing.T) {
	b := &countingBody{closed: make(chan struct{}), readEnd: make(chan struct{})}
	x := &VaultExecutor{Clock: realClock{}, Transport: stuckTransport(b), Secrets: fakeSecrets{"s"}}
	mustReturn(t, runAsync(x, shortVault(30, 10)), 5*time.Second)
	select {
	case <-b.readEnd:
	case <-time.After(5 * time.Second):
		t.Fatal("the read was not released")
	}
	time.Sleep(100 * time.Millisecond) // the exchange goroutine's own deferred close
	if n := b.closes.Load(); n != 1 {
		t.Fatalf("closed %d times", n)
	}
}

// A transport that answers with no body and no error is a failure, not a crash.
func TestANilBodyIsATransportFailure(t *testing.T) {
	tr := fakeTransport{func(context.Context, VaultArgs, []byte) (Response, error) { return Response{Status: 204}, nil }}
	r := newVRig(t, tr)
	ch := r.start(testVault, context.Background())
	res := r.finish(ch, 100*time.Millisecond)
	if res.err != nil || res.env.Outcome != OutcomeTransportError {
		t.Fatalf("%+v %v", res.env, res.err)
	}
}
