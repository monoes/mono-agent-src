//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
	"github.com/monoes/mono-agent/internal/storage"
)

// blockTurn is a turn that runs until end is called.
type blockTurn struct {
	done chan struct{}
	once sync.Once
}

func (*blockTurn) PID() int      { return 4343 }
func (*blockTurn) Stamp() string { return "s" }
func (t *blockTurn) Wait() error { <-t.done; return nil }
func (t *blockTurn) Stop()       { t.end() }
func (t *blockTurn) end()        { t.once.Do(func() { close(t.done) }) }
func newBlockTurn() *blockTurn   { return &blockTurn{done: make(chan struct{})} }

// blockLauncher hands out blockTurns and counts the process starts it was asked for.
type blockLauncher struct {
	mu     sync.Mutex
	starts int
	turns  []*blockTurn
	err    error
}

func (l *blockLauncher) Start(string, string, string, string, []string, string) (coderhost.Turn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	l.starts++
	t := newBlockTurn()
	l.turns = append(l.turns, t)
	return t, nil
}
func (*blockLauncher) Adopt(int, string) coderhost.Turn      { return nil }
func (*blockLauncher) Finish(string, string, string, string) {}
func (l *blockLauncher) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}
func (l *blockLauncher) turn(i int) *blockTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turns[i]
}

func govCharter(extraLimits string) string {
	return `{"goal":"ship it","limits":{"budget_micro":5000000,"max_concurrent":2,"live_per_tree":2,"max_total":100` + extraLimits + `},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"}}`
}

type govRig struct {
	*wireRig
	db   *storage.Database
	l    *blockLauncher
	h    *coderhost.Host
	gate *HostGate
	gw   *GovernorWire
	smp  *machine.Fake
}

// newGovRig builds the host rig with the real gate and the real governor over a scratch database; only the machine
// (a scripted sampler) and the process (a blocking turn) are fakes.
func newGovRig(t *testing.T, limits string, steps ...machine.Step) *govRig {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.charter = writeCharter(t, govCharter(limits))
	l := &blockLauncher{}
	h := &coderhost.Host{Store: w.host, L: l}
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, BindingOn: func() (bool, error) { return true, nil }}
	g.Install(w.host, h)
	if len(steps) == 0 {
		steps = []machine.Step{{Sample: machine.Healthy()}}
	}
	smp := machine.NewFake(steps...)
	gw := &GovernorWire{DB: db.DB, Profile: "p1", Keys: w.w.Witness.Keys, Runs: w.w.Chained(), RunDir: w.w.RunDir, Sampler: smp,
		Logf: func(f string, a ...any) { t.Logf(f, a...) }}
	h.L = gw.GovernLauncher(h.L)
	return &govRig{wireRig: w, db: db, l: l, h: h, gate: g, gw: gw, smp: smp}
}

func (r *govRig) startTurn(id string) (coderhost.Turn, error) {
	return r.h.L.Start("p1", "conv-"+id, "t-"+id, "hi", nil, "")
}

func (r *govRig) liveSlots(t *testing.T) int {
	return count(t, r.db, `SELECT COUNT(*) FROM doomsday_slots WHERE released_at IS NULL`)
}

func (r *govRig) tickets(t *testing.T) int {
	return count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'ticket.started'`)
}

// With no open run the governor is a plain call: nothing is built, written or refused.
func TestGovernorWithNoRunChangesNothing(t *testing.T) {
	r := newGovRig(t, "")
	for i := 0; i < 5; i++ {
		if _, err := r.startTurn(fmt.Sprint(i)); err != nil {
			t.Fatalf("a start was refused with no run: %v", err)
		}
	}
	if r.l.count() != 5 || r.smp.Calls() != 0 {
		t.Fatalf("starts %d, sampler calls %d", r.l.count(), r.smp.Calls())
	}
	for _, tbl := range []string{"doomsday_slots", "doomsday_queue", "doomsday_governor_state"} {
		if n := count(t, r.db, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Fatalf("%s has %d rows with no run", tbl, n)
		}
	}
}

// Concurrent launches never exceed the charter cap; the one that does not fit is deferred (not failed), costs no ticket and
// no process, and starts when a slot is freed.
func TestGovernorHoldsTheCapDefersTheRestAndLeaksNoTicket(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	for _, id := range []string{"a", "b"} {
		tn, err := r.startTurn(id)
		if err != nil {
			t.Fatalf("turn %s: %v", id, err)
		}
		go func() { _ = tn.Wait() }() // the host's watcher
	}
	tickets := r.tickets(t)
	_, err := r.startTurn("c")
	if !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatalf("the third turn under a cap of 2: %v, want deferred", err)
	}
	if r.l.count() != 2 || r.liveSlots(t) != 2 || r.tickets(t) != tickets {
		t.Fatalf("starts %d, live slots %d, tickets %d (was %d): a deferred launch must start nothing and take no ticket", r.l.count(), r.liveSlots(t), r.tickets(t), tickets)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue WHERE state = 'queued'`); n != 1 {
		t.Fatalf("%d queued rows, want the one waiting launch", n)
	}
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) || count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue`) != 1 {
		t.Fatalf("asking again must not enqueue twice: %v", err)
	}
	r.l.turn(0).end() // the first turn ends: its Wait releases the slot
	waitUntil(t, "a slot to be free", func() bool { return r.liveSlots(t) == 1 })
	if _, err := r.startTurn("c"); err != nil {
		t.Fatalf("after a slot was freed: %v", err)
	}
	if r.liveSlots(t) != 2 || r.l.count() != 3 {
		t.Fatalf("live slots %d, starts %d", r.liveSlots(t), r.l.count())
	}
}

// The line is fair: a slot that frees while a launch waits goes to the one waiting, not to a fresh arrival.
func TestAFreshArrivalDoesNotJumpTheQueue(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	var first coderhost.Turn
	for _, id := range []string{"a", "b"} {
		tn, err := r.startTurn(id)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = tn
		}
	}
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatalf("c: %v", err)
	}
	go func() { _ = first.Wait() }()
	r.l.turn(0).end()
	waitUntil(t, "a slot to be free", func() bool { return r.liveSlots(t) == 1 })
	if _, err := r.startTurn("d"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatalf("a fresh arrival took the slot the waiting launch was owed: %v", err)
	}
	if _, err := r.startTurn("c"); err != nil {
		t.Fatalf("the waiting launch did not get the freed slot: %v", err)
	}
}

// Wait on the governed turn is what frees the slot, so the host's watcher releases it.
func TestGovernedTurnReleasesItsSlotOnWaitAndOnlyOnce(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	tn, err := r.startTurn("a")
	if err != nil {
		t.Fatal(err)
	}
	var waited atomic.Bool
	go func() { _ = tn.Wait(); waited.Store(true) }()
	if r.liveSlots(t) != 1 {
		t.Fatal("no slot taken")
	}
	r.l.turn(0).end()
	waitUntil(t, "release", func() bool { return waited.Load() && r.liveSlots(t) == 0 })
	tn.Stop() // a second end must not release again or fail
	if r.liveSlots(t) != 0 {
		t.Fatal("slots after a double end")
	}
}

// A start the dispatch gate refuses (or the launcher fails) gives the slot back: nothing leaks.
func TestAFailedOrRefusedStartLeaksNoSlot(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	r.l.err = errors.New("no such runtime")
	for i := 0; i < 4; i++ { // more than the cap: a leak would defer from the third on
		if _, err := r.startTurn(fmt.Sprint(i)); err == nil || errors.Is(err, coderhost.ErrDeferred) {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if r.liveSlots(t) != 0 {
		t.Fatalf("%d slots leaked by failed starts", r.liveSlots(t))
	}
	r.l.err = nil
	r.gate.BindingOn = func() (bool, error) { return false, nil } // the gate refuses a turn that is not bound
	for i := 0; i < 4; i++ {
		if _, err := r.startTurn("g" + fmt.Sprint(i)); !refused(err) {
			t.Fatalf("a gate refusal %d: %v", i, err)
		}
	}
	if r.liveSlots(t) != 0 || r.l.count() != 0 {
		t.Fatalf("slots %d, starts %d after gate refusals", r.liveSlots(t), r.l.count())
	}
}

// Stop wins: once the run is not running, nothing is admitted, queued or reserved, even with a slot free.
func TestGovernorAdmitsNothingAfterTheMarker(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	if _, err := r.startTurn("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.RunDir.WriteMarker(1, "", "test"); err != nil {
		t.Fatal(err)
	}
	// every slot row ever written, released ones included: a reserve-then-give-back after Stop would show here
	slots, queued := count(t, r.db, `SELECT COUNT(*) FROM doomsday_slots`), count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue`)
	for i := 0; i < 3; i++ {
		if _, err := r.startTurn("x" + fmt.Sprint(i)); !refused(err) {
			t.Fatalf("a start after the marker: %v", err)
		}
	}
	if count(t, r.db, `SELECT COUNT(*) FROM doomsday_slots`) != slots || count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue`) != queued || r.l.count() != 1 {
		t.Fatal("the governor reserved, queued or started something after Stop")
	}
}

// A machine that cannot be read lowers to the safe minimum named in the charter; it never admits up to the cap.
func TestGovernorLowersToTheSafeMinimumWhenTheMachineCannotBeRead(t *testing.T) {
	r := newGovRig(t, `,"safe_min_concurrent":1`, machine.Step{Err: errors.New("proc unreadable")})
	r.start(t)
	if _, err := r.startTurn("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.startTurn("b"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatalf("a second turn with an unreadable machine and safe minimum 1: %v", err)
	}
	if r.liveSlots(t) != 1 {
		t.Fatalf("%d slots", r.liveSlots(t))
	}
}

// The queue is bounded by the charter: a full queue is a visible refusal and writes nothing.
func TestGovernorQueueIsBoundedByTheCharter(t *testing.T) {
	r := newGovRig(t, `,"queue_max_rows":1`)
	r.start(t)
	for _, id := range []string{"a", "b"} {
		if _, err := r.startTurn(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.startTurn("c"); !errors.Is(err, coderhost.ErrDeferred) {
		t.Fatalf("first waiting launch: %v", err)
	}
	_, err := r.startTurn("d")
	if !errors.Is(err, coderhost.ErrLimit) || errors.Is(err, coderhost.ErrDeferred) || !strings.Contains(err.Error(), "queue") {
		t.Fatalf("a launch past the queue bound: %v, want a visible limit refusal", err)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue`); n != 1 {
		t.Fatalf("%d queue rows after a refusal, want 1", n)
	}
}

// A slot whose turn is gone with nobody to release it (a crash) is given back by the reconcile, and a slot of a turn that is
// still alive is not.
func TestOrphanSlotsAreReconciledAndLiveOnesAreKept(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	for _, id := range []string{"gone", "alive"} {
		if _, err := r.startTurn(id); err != nil {
			t.Fatal(err)
		}
	}
	// the host process "restarts": a new wire over the same database knows no turn of its own
	gw2 := &GovernorWire{DB: r.db.DB, Profile: "p1", Keys: r.gw.Keys, Runs: r.gw.Runs, RunDir: r.gw.RunDir, Sampler: r.smp,
		Alive: func(conv, turn string) bool { return conv == "conv-alive" }}
	gw2.Tick(context.Background())
	if n := r.liveSlots(t); n != 1 {
		t.Fatalf("%d live slots after the reconcile, want only the live turn's", n)
	}
	if n := count(t, r.db, `SELECT COUNT(*) FROM doomsday_slots WHERE released_at IS NULL AND member_id LIKE '%alive%'`); n != 1 {
		t.Fatal("the live turn's slot was released")
	}
}

// Stop's token seam cancels the governor's queue with the host's, and a wedged governor cannot keep Stop from completing.
func TestStopCancelsTheQueueAndAWedgedGovernorDoesNotBlockIt(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = r.startTurn(id)
	}
	inner := &countingTokens{}
	g := GovernedTokens{Inner: inner, DB: r.db.DB, Profile: "p1", Keys: r.gw.Keys}
	if err := g.CancelQueue(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if inner.cancels != 1 || count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue WHERE state = 'queued'`) != 0 || count(t, r.db, `SELECT COUNT(*) FROM doomsday_queue WHERE state = 'cancelled'`) != 1 {
		t.Fatalf("cancels %d; the waiting launch must be cancelled", inner.cancels)
	}
	// wedged: the governor's database is gone; the inner result is still the answer, and quickly
	r.db.DB.Close()
	start := time.Now()
	g2 := GovernedTokens{Inner: &countingTokens{err: errors.New("inner")}, DB: r.db.DB, Profile: "p1", Keys: r.gw.Keys}
	if err := g2.CancelQueue(context.Background(), 1); err == nil || err.Error() != "inner" {
		t.Fatalf("the inner error must be Stop's answer, got %v", err)
	}
	if time.Since(start) > 2*govCallTimeout {
		t.Fatal("a broken governor held Stop")
	}
	g3 := GovernedTokens{Inner: &countingTokens{}, DB: r.db.DB, Profile: "p1", Keys: r.gw.Keys}
	if err := g3.CancelQueue(context.Background(), 1); err != nil {
		t.Fatalf("a broken governor failed Stop: %v", err)
	}
}

type countingTokens struct {
	cancels int
	err     error
}

func (c *countingTokens) RevokeTokens(context.Context, uint64) error { return nil }
func (c *countingTokens) CancelQueue(context.Context, uint64) error  { c.cancels++; return c.err }

// The status verb shows the cap, the counts and the host's last report, writes nothing, and carries no member id.
func TestGovernorStatusIsReadOnlyAndShowsTheHostsReport(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	for _, id := range []string{"a", "b", "c"} {
		_, _ = r.startTurn(id)
	}
	r.gw.Tick(context.Background())
	rd := GovernorReader{DB: r.db.DB, Profile: "p1", Runs: r.w.Chained()}
	var before int
	_ = r.db.DB.QueryRow(`SELECT total_changes()`).Scan(&before)
	v, err := rd.Read(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var after int
	_ = r.db.DB.QueryRow(`SELECT total_changes()`).Scan(&after)
	if before != after {
		t.Fatal("the status read wrote to the database")
	}
	if v.Cap != 2 || v.LiveProcess != 2 || v.QueueRows != 1 || !v.HostReporting || v.Effective == nil || *v.Effective > 2 {
		t.Fatalf("view: %+v", v)
	}
	cmd := GovernorCommands(Deps{JSONOutput: func() bool { return true }}, func(ctx context.Context, g int64) (GovernorView, error) { return rd.Read(ctx, g) })
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "turn|") || strings.Contains(out.String(), "conv-") {
		t.Fatalf("the view leaks a member or conversation id: %s", out.String())
	}
	// off: no run
	cmd2 := GovernorCommands(Deps{JSONOutput: func() bool { return false }}, nil)
	var out2 strings.Builder
	cmd2.SetOut(&out2)
	cmd2.SetArgs([]string{"status"})
	if err := cmd2.Execute(); err != nil || !strings.Contains(out2.String(), "off") {
		t.Fatalf("no reader must say off: %v %q", err, out2.String())
	}
}

// A stale or absent report shows no decision: the host is not reporting.
func TestGovernorStatusDoesNotShowAStaleReport(t *testing.T) {
	r := newGovRig(t, "")
	r.start(t)
	_, _ = r.startTurn("a")
	r.gw.Tick(context.Background())
	rd := GovernorReader{DB: r.db.DB, Profile: "p1", Runs: r.w.Chained(), Now: func() time.Time { return time.Now().Add(2 * GovernorStaleAfter) }}
	v, err := rd.Read(context.Background(), 0)
	if err != nil || v.HostReporting || v.Effective != nil {
		t.Fatalf("a stale report was shown: %+v %v", v, err)
	}
	// a report that claims more than the cap is clamped in the view
	_, _ = r.db.DB.Exec(`UPDATE doomsday_governor_state SET value = ? WHERE key = 'snapshot'`, fmt.Sprintf(`{"at":%d,"cap":99,"effective":99,"admitting":true}`, time.Now().Unix()))
	v, _ = GovernorReader{DB: r.db.DB, Profile: "p1", Runs: r.w.Chained()}.Read(context.Background(), 0)
	if v.Effective == nil || *v.Effective > v.Cap {
		t.Fatalf("the view shows an effective limit above the charter cap: %+v", v)
	}
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var _ governor.Sampler = (*machine.Fake)(nil)
