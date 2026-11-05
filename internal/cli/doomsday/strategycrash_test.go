package doomsday

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Crash windows of the strategy layer on the real witness. A "crash" is a panic raised at one of three points around a
// witnessed write: seq.pending (the pending line is down, the write has not run), seq.written (the database committed,
// the committed line is not down) and after (everything is down). A fresh host is built over the same database, as after a
// restart, and must repair or continue without a duplicate or lost epoch, a lost or doubled marker, or a witness that no
// longer accepts the generation. The rig's judge is fakeJudge of strategye2e_test.go.

type crashSentinel struct{}

type crasher struct {
	mu     sync.Mutex
	point  string
	target int
	n      int
	armed  bool
	total  map[string]int
}

func (c *crasher) hook(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total[p]++
	if !c.armed || p != c.point {
		return
	}
	if c.n++; c.n == c.target {
		c.armed = false
		panic(crashSentinel{})
	}
}

func (c *crasher) arm(point string, target int) {
	c.mu.Lock()
	c.point, c.target, c.n, c.armed = point, target, 0, true
	c.mu.Unlock()
}

// crashSeq raises the "after" crash once the wrapped transition returned.
type crashSeq struct {
	inner sstore.Sequencer
	c     *crasher
}

func (s crashSeq) SequenceRows(ctx context.Context, gen int64, kind string, rows int, fn func(context.Context) error) error {
	err := s.inner.SequenceRows(ctx, gen, kind, rows, fn)
	s.c.mu.Lock()
	s.c.total["after"]++
	fire := false
	if s.c.armed && s.c.point == "after" {
		if s.c.n++; s.c.n == s.c.target {
			s.c.armed, fire = false, true
		}
	}
	s.c.mu.Unlock()
	if fire {
		panic(crashSentinel{})
	}
	return err
}

func crashed(f func()) (did bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crashSentinel); !ok {
				panic(r)
			}
			did = true
		}
	}()
	f()
	return false
}

func newCrashRig(t *testing.T, c *crasher) *stratRig {
	r := newStratRig(t, finishCharterJSON, func(d *StrategyHostDeps) {
		if ws, ok := d.Seq.(witnessSeq); ok {
			ws.s.Hook = c.hook
		}
		d.Seq = crashSeq{inner: d.Seq, c: c}
	})
	r.judge.set(types.CritPlausiblyMet)
	return r
}

// restarted is a new host over the same database with the crash hooks gone.
func (r *stratRig) restarted(t *testing.T) *StrategyHost {
	t.Helper()
	d := r.deps
	d.Seq = d.Seq.(crashSeq).inner
	h, err := NewStrategyHost(d)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (r *stratRig) chainAndWitnessAccept(t *testing.T) {
	t.Helper()
	if err := r.w.Store.VerifyAudit(bg, "p1", 1); err != nil {
		t.Errorf("chain: %v", err)
	}
	seq, err := r.w.Sequencer(bg)
	if err != nil {
		t.Fatal(err)
	}
	if err := seq.Check(bg, 1); err != nil {
		t.Errorf("the witness does not accept the generation: %v", err)
	}
}

// Every witnessed write of an epoch tick (open, judge record, apply, applied, ready marker) crashed at every point: after
// the restart there is exactly one epoch, exactly one ready marker, at most two judge calls (a crash between the answer and
// its record costs a second call), the run is still running (nothing begins a finish), and the witness accepts the chain.
func TestStrategyCrashAtEveryWitnessedWriteOfAnEpochTick(t *testing.T) {
	var N int
	{
		c := &crasher{total: map[string]int{}}
		r := newCrashRig(t, c)
		if res, err := r.propose(t, "dir one"); err != nil || !res.Accepted {
			t.Fatal(res, err)
		}
		before := c.total["seq.pending"]
		r.tickAfter(t, 31*time.Minute)
		N = c.total["seq.pending"] - before
		if N < 4 {
			t.Fatalf("only %d witnessed writes in an epoch tick: the matrix would prove little", N)
		}
	}
	for _, point := range []string{"seq.pending", "seq.written", "after"} {
		for k := 1; k <= N; k++ {
			t.Run(fmt.Sprintf("%s-%d", point, k), func(t *testing.T) {
				c := &crasher{total: map[string]int{}}
				r := newCrashRig(t, c)
				if res, err := r.propose(t, "dir one"); err != nil || !res.Accepted {
					t.Fatal(res, err)
				}
				c.arm(point, k)
				r.clock.Advance(31 * time.Minute)
				if !crashed(func() { _, _ = r.host.Tick(bg) }) {
					t.Fatal("the crash did not fire")
				}
				h2 := r.restarted(t)
				for i := 0; i < 4; i++ {
					if r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) == 0 {
						r.clock.Advance(31 * time.Minute) // the crash came before the epoch was opened
					}
					if _, err := h2.Tick(bg); err != nil {
						t.Logf("restart tick: %v", err)
					}
				}
				if n := r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`); n != 1 {
					t.Errorf("%d epochs after the restart", n)
				}
				if n := r.ready(t); n != 1 {
					t.Errorf("%d ready markers after the restart", n)
				}
				if r.judge.n() > 2 {
					t.Errorf("judge calls %d", r.judge.n())
				}
				r.notFinished(t)
				r.chainAndWitnessAccept(t)
			})
		}
	}
}

// A proposal is two chained rows in ONE witnessed transition (the direction line and the novelty line). Crashed at each
// point, the retry after the restart either finds the direction or makes it, never two, and the witness accepts the
// generation. The seq.written case is the one that fails closed at row 5 if the transition declares one row instead of two:
// no clean commit shows it, only a crash between the database commit and the committed line does.
func TestStrategyCrashAtTheProposalsWitnessedWrite(t *testing.T) {
	for _, point := range []string{"seq.pending", "seq.written", "after"} {
		t.Run(point, func(t *testing.T) {
			c := &crasher{total: map[string]int{}}
			r := newCrashRig(t, c)
			c.arm(point, 1)
			prop := ops.StrategyProposal{Profile: "p1", Generation: 1, Proposal: types.Proposal{Title: "dir one", Rationale: "x", CriterionIDs: []string{"c0"}, ProposerSession: "session:1"}}
			if !crashed(func() { _, _ = r.host.Sink().Propose(bg, prop) }) {
				t.Fatal("the crash did not fire")
			}
			h2 := r.restarted(t)
			if _, err := h2.Sink().Propose(bg, prop); err != nil {
				t.Fatalf("the retry after the crash: %v", err)
			}
			if n := r.count(t, `SELECT COUNT(*) FROM doomsday_strategy_directions`); n != 1 {
				t.Errorf("%d directions after the retry", n)
			}
			r.chainAndWitnessAccept(t)
			// and a write after the repair is accepted by the witness too
			if _, err := h2.Sink().Propose(bg, ops.StrategyProposal{Profile: "p1", Generation: 1, Proposal: types.Proposal{Title: "dir two", Rationale: "x", CriterionIDs: []string{"c0"}, ProposerSession: "session:1"}}); err != nil {
				t.Fatalf("a write after the repair: %v", err)
			}
			r.chainAndWitnessAccept(t)
		})
	}
}

// After the run has quiesced the strategy layer writes nothing further: the same crash matrix is not needed, but the
// witness must accept the generation once the abandonment of an open epoch is the only write.
func TestStrategyStopThenWitness(t *testing.T) {
	c := &crasher{total: map[string]int{}}
	r := newCrashRig(t, c)
	if _, _, err := r.host.store.InsertEpoch(bg, types.Epoch{Profile: "p1", Generation: 1, Seq: 1, Trigger: types.TriggerTimer, StartedAt: r.clock.Now().Unix(), State: types.EpochOpen, ModelPin: "p/m@1"}); err != nil {
		t.Fatal(err)
	}
	if out, err := r.do(t, "stop"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if run, _ := r.w.Store.GetRun(bg, "p1", 1); run.State != store.StateQuiescent {
		t.Fatalf("the run is %s", run.State)
	}
	r.tickAfter(t, 40*time.Minute)
	if ep, err := r.host.store.GetEpoch(bg, "p1", 1, 1); err != nil || ep.State != types.EpochAbandoned {
		t.Fatalf("%v %v", ep.State, err)
	}
	if n := r.ready(t); n != 0 {
		t.Fatalf("%d ready markers after the stop", n)
	}
	r.chainAndWitnessAccept(t)
}
