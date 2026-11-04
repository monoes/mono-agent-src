package stop

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is the injected clock: time moves only when a test advances it.
type fakeClock struct {
	mu   sync.Mutex
	now  time.Time
	subs []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_800_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.subs = append(c.subs, fakeTimer{at: c.now.Add(d), ch: ch})
	return ch
}

// waiters is how many timers are registered and not yet fired.
func (c *fakeClock) waiters() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subs)
}

// waitTimers spins until more than before timers are registered: the code under test has reached its wait on the clock, so
// an Advance after it cannot be lost (an Advance before it fires nothing and the wait then never ends).
func waitTimers(t *testing.T, c *fakeClock, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.waiters() <= before {
		if time.Now().After(deadline) {
			t.Fatal("the code under test never waited on the clock")
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var keep []fakeTimer
	for _, s := range c.subs {
		if !s.at.After(c.now) {
			s.ch <- c.now
		} else {
			keep = append(keep, s)
		}
	}
	c.subs = keep
	c.mu.Unlock()
}

// pauser is the deterministic pause-point hook: one-shot blocks, no sleeps.
type pauser struct {
	mu    sync.Mutex
	armed map[string]*pausePoint
}

type pausePoint struct{ reached, release chan struct{} }

func newPauser() *pauser { return &pauser{armed: map[string]*pausePoint{}} }

func (p *pauser) arm(point string) *pausePoint {
	pp := &pausePoint{reached: make(chan struct{}), release: make(chan struct{})}
	p.mu.Lock()
	p.armed[point] = pp
	p.mu.Unlock()
	return pp
}

func (p *pauser) hook(point string) {
	p.mu.Lock()
	pp := p.armed[point]
	delete(p.armed, point)
	p.mu.Unlock()
	if pp != nil {
		close(pp.reached)
		<-pp.release
	}
}

func (pp *pausePoint) wait()   { <-pp.reached }
func (pp *pausePoint) resume() { close(pp.release) }

// fakeRegistry records what the gate writes and fails on demand.
type fakeRegistry struct {
	mu          sync.Mutex
	started     []uint64
	aborted     map[uint64]string // ticket -> approval
	unknown     []uint64
	failStarted atomic.Bool
	failAborted atomic.Bool
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{aborted: map[uint64]string{}} }

var errInjected = errors.New("injected failure")

func (r *fakeRegistry) RecordStarted(t TicketInfo, _ string) error {
	if r.failStarted.Load() {
		return errInjected
	}
	r.mu.Lock()
	r.started = append(r.started, t.ID)
	r.mu.Unlock()
	return nil
}

func (r *fakeRegistry) RecordAborted(t TicketInfo, approval string) error {
	if r.failAborted.Load() {
		return errInjected
	}
	r.mu.Lock()
	r.aborted[t.ID] = approval
	r.mu.Unlock()
	return nil
}

func (r *fakeRegistry) RecordUnknown(t TicketInfo) error {
	r.mu.Lock()
	r.unknown = append(r.unknown, t.ID)
	r.mu.Unlock()
	return nil
}

func (r *fakeRegistry) abortedApproval(id uint64) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.aborted[id]
	return a, ok
}

// fakeHeld is a held child: created, never run until Release.
type fakeHeld struct {
	released, killed atomic.Bool
	killErr          error
}

func (h *fakeHeld) Handle() string { return "pid:1" }
func (h *fakeHeld) Release() error { h.released.Store(true); return nil }
func (h *fakeHeld) Kill() error    { h.killed.Store(true); return h.killErr }

type gateFixture struct {
	g     *Gate
	reg   *fakeRegistry
	clock *fakeClock
	p     *pauser
}

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	f := &gateFixture{reg: newFakeRegistry(), clock: newFakeClock(), p: newPauser()}
	f.g = NewGate(GateConfig{Generation: 7, Reg: f.reg, Clock: f.clock, Hook: f.p.hook})
	return f
}

// closeAsync runs Close in a goroutine and returns the channel of its outcome.
func closeAsync(g *Gate) <-chan Outcome {
	ch := make(chan Outcome, 1)
	go func() { ch <- g.Close() }()
	return ch
}

// waitSealed spins until the gate is sealed (Close has begun), without sleeping on wall time.
func waitSealed(t *testing.T, g *Gate) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !g.Sealed() {
		if time.Now().After(deadline) {
			t.Fatal("gate never sealed")
		}
		time.Sleep(time.Millisecond)
	}
}

func mustAdmitStart(t *testing.T, g *Gate, spec Spec) *Ticket {
	t.Helper()
	tk, err := g.Admit(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.BeginStart(tk); err != nil {
		t.Fatal(err)
	}
	return tk
}
