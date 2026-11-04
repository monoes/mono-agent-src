package dynorg

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A paused session's runs go on, so their rows must be renewed in every live state: a cap run's row that lapsed would stop
// latching (a reservation of another process could be granted beside a spending run), and a reservation that lapsed
// would be expired and its final charge refused.
func TestPausedSessionKeepsItsAccountingRowsAlive(t *testing.T) {
	for _, mode := range []string{"cap", "reserve"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t, rigOpts{budget: 10, lease: 24 * time.Hour})
			r.mc.WorkerReservations = func(context.Context) (bool, error) { return mode == "reserve", nil }
			r.c = r.clockedConductor(Limits{})
			a := r.add(spec{title: "a", files: "a.go"})
			r.approve()
			r.ex.onRun = func(ctx context.Context, id int64, on func(monomind.Event)) { on(usageCost(0.1)) }
			r.next(r.c)
			eventually(t, "the run is in flight", func() bool {
				return r.capRuns()+len(r.resvRows()) == 1 && spendNear(r.session().SpentUSD, 0.1) || mode == "cap" && r.capRuns() == 1
			})
			if _, err := r.store.PauseSession(tbg, "default", r.sid, operator); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 5; i++ { // 250s in all, past the lease and the grace, one heartbeat interval at a time
				r.clk.advance(50 * time.Second)
				time.Sleep(80 * time.Millisecond) // several ticks (10ms apart)
			}
			if mode == "reserve" {
				if rows := r.resvRows(); len(rows) != 1 || rows[0].state != tasks.ReservationLive {
					t.Fatalf("the reservation lapsed under a paused session: %+v", rows)
				}
			} else if _, err := r.store.ReserveExec(tbg, "default", r.sid, "other-process", "w-x", "e1", 1); !errors.Is(err, tasks.ErrAccountingMode) {
				t.Fatalf("the cap run's row lapsed under a paused session and let a reservation in: %v", err)
			}
			r.ex.release(a.ID)
			eventually(t, "the run to end", func() bool {
				if mode == "reserve" {
					return r.resvRows()[0].state == tasks.ReservationSettled
				}
				return r.capRuns() == 0
			})
		})
	}
}

// What a run that FAILED costs. Start failure (the runtime never started: no start event, no result): $0, so a broken
// start retried N times burns nothing. Started and then died (a start event, no cost): the whole grant, nothing says it
// cost less. Started and streamed some cost: that cost. Ambiguous (a result came back but no start event was seen, no
// cost): the whole grant, never an undercount. Stopped before the runtime started: $0.
func TestFailedRunsAreChargedByHowFarTheyGot(t *testing.T) {
	boom := errors.New("auth failed")
	cases := []struct {
		name string
		step step
		want float64
	}{
		{"start failure", step{noStart: true, err: boom}, 0},
		{"stopped before it started", step{noStart: true, block: true}, 0},
		{"started then died", step{err: boom}, missionExecAskUSD},
		{"died after a streamed cost", step{stream: []float64{0.3}, err: boom}, 0.3},
		{"ambiguous start", step{noStart: true, noCost: true}, missionExecAskUSD},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newResvRig(t, rigOpts{budget: 100})
			sx := &scripted{steps: []step{tc.step, tc.step, tc.step}}
			c := scriptedConductor(r, sx)
			w := directWorker(r, "w-1")
			r.approve()
			for i := 0; i < 3; i++ { // the scheduler retries a failed run
				ctx, cancel := context.WithCancel(tbg)
				if tc.step.block {
					time.AfterFunc(20*time.Millisecond, cancel)
				}
				_, _ = c.execOnce(ctx, w, opus, "doing task", "")
				cancel()
			}
			if got := r.session().SpentUSD; !spendNear(got, 3*tc.want) {
				t.Fatalf("3 runs: spent %v, want 3 x %v", got, tc.want)
			}
			for _, x := range r.resvRows() {
				if x.state == tasks.ReservationLive {
					t.Fatalf("a row was left live: %+v", x)
				}
			}
		})
	}
}

// A charge that fails is noticed once per failure class for the run, is not retried per event (the stream is never held
// up), and lands at the settle. A cost that is not a finite number of dollars is ignored.
func TestChargeFailureIsNoticedOnceAndRetriedAtTheSettle(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 100})
	if _, err := r.db.Exec(`CREATE TRIGGER block_charge BEFORE UPDATE OF charged_usd ON task_session_reservations WHEN NEW.state = 'live'
		BEGIN SELECT RAISE(ABORT, 'database is busy'); END`); err != nil {
		t.Fatal(err)
	}
	sx := &scripted{steps: []step{{stream: []float64{0.1, 0.2, 0.3, 0.4, 0.5}, final: 0.5}}}
	c := scriptedConductor(r, sx)
	w := directWorker(r, "w-1")
	r.approve()
	r.runOnce(c, w, tbg)
	if n := countNotices(r.em, "mission.spend"); n != 1 {
		t.Fatalf("%d notices for one failing run, want 1", n)
	}
	if got := r.session().SpentUSD; !spendNear(got, 0.5) {
		t.Fatalf("the settle records what the stream could not: spent %v", got)
	}
}

func TestNonFiniteAndNegativeCostsAreIgnored(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 100})
	c := scriptedConductor(r, &scripted{})
	w := directWorker(r, "w-1")
	r.approve()
	a, err := c.enterRun(tbg, w, &monomind.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		c.chargeLive(w, bad)
	}
	c.chargeLive(w, 0.25)
	w.task.spendMu.Lock()
	peak := a.peak
	w.task.spendMu.Unlock()
	if peak != 0.25 || !spendNear(r.session().SpentUSD, 0.25) {
		t.Fatalf("peak %v spent %v: a bad figure poisoned the run", peak, r.session().SpentUSD)
	}
	c.endRun(w, a)
}
