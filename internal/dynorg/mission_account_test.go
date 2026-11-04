package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Across several runs, a fallback model, a stop in the middle of a run and a settle after partial charges, the session
// is charged each run's cost once, and every reservation's charge is its own run's cost (a per-exec model: nothing is
// measured from the worker's cumulative counter).
func TestWorkerReservationEachRunIsChargedItsOwnCostOnce(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	sx := &scripted{steps: []step{
		{stream: []float64{0.1, 0.4}, final: 0.5},    // run 1: partial charges, the settle adds the rest
		{stream: []float64{0.3}, final: 0.3},         // run 2 (the fallback model): streamed = final
		{final: 0.2},                                 // run 3: nothing streamed, all at settle
		{stream: []float64{0.15, 0.25}, block: true}, // run 4: stopped mid-run; the runtime still reports 0.25 at the end
		{stream: []float64{0.05}, final: 0.05},       // run 5, after the stop
	}}
	sx.steps[3].final = 0.25
	c := scriptedConductor(r, sx)
	w := directWorker(r, "w-1")
	r.approve()
	want := 0.0
	for i, cost := range []float64{0.5, 0.3, 0.2} {
		r.runOnce(c, w, tbg)
		want += cost
		if got := r.session().SpentUSD; !spendNear(got, want) {
			t.Fatalf("after run %d: spent %v, want %v (once)", i+1, got, want)
		}
	}
	ctx, cancel := context.WithCancel(tbg)
	done := make(chan struct{})
	go func() { r.runOnce(c, w, ctx); close(done) }()
	eventually(t, "run 4's partial charge", func() bool { return spendNear(r.session().SpentUSD, want+0.25) })
	c.chargeSession(w) // the watcher's tick: a reserving run's cost is never added to the session a second time
	if got := r.session().SpentUSD; !spendNear(got, want+0.25) {
		t.Fatalf("double charge: spent %v, want %v", got, want+0.25)
	}
	cancel()
	<-done
	want += 0.25
	r.runOnce(c, w, tbg)
	want += 0.05
	if got := r.session().SpentUSD; !spendNear(got, want) {
		t.Fatalf("at the end: spent %v, want %v", got, want)
	}
	rows := r.resvRows()
	if len(rows) != 5 {
		t.Fatalf("one reservation per run: %+v", rows)
	}
	for i, cost := range []float64{0.5, 0.3, 0.2, 0.25, 0.05} {
		if x := rows[i]; x.state != tasks.ReservationSettled || !spendNear(x.charge, cost) || x.ref != "w-1" {
			t.Errorf("reservation %d = %+v, want settled at %v", i+1, x, cost)
		}
	}
	if w.task.acct != nil {
		t.Fatal("nothing is left in flight")
	}
}

// A final cost below what was streamed: the run is charged its peak (the store never lowers a charge), and the next run
// is charged its own cost in full, in either mode. On the cap the baseline is re-based to the counter; left at the
// streamed figure the next run's first dollars would read as recorded already (a lost charge).
func TestLowerFinalCostLosesNoCharge(t *testing.T) {
	for _, mode := range []string{"legacy", "cap", "reserve"} {
		t.Run(mode, func(t *testing.T) {
			r := newRig(t, rigOpts{budget: 100})
			if mode != "legacy" { // legacy: no switch wired at all, nothing registered
				r.mc.WorkerReservations = func(context.Context) (bool, error) { return mode == "reserve", nil }
			}
			sx := &scripted{steps: []step{
				{stream: []float64{0.4}, final: 0.2},       // charged 0.4 (the peak)
				{stream: []float64{0.3}, final: 0.3},       // its own 0.3 in full -> 0.7
				{stream: []float64{0.5}, final: 0},         // final 0: the streamed 0.5 stays -> 1.2
				{stream: []float64{0.3, 0.2}, final: 0.25}, // cost falling between reports: peak 0.3 -> 1.5
				{final: 0.1}, // nothing streamed, all at the end -> 1.6
			}}
			c := scriptedConductor(r, sx)
			w := directWorker(r, "w-1")
			r.approve()
			want := 0.0
			for i, cost := range []float64{0.4, 0.3, 0.5, 0.3, 0.1} {
				r.runOnce(c, w, tbg)
				want += cost
				if got := r.session().SpentUSD; !spendNear(got, want) {
					t.Fatalf("after run %d: spent %v, want %v", i+1, got, want)
				}
			}
		})
	}
}

// Carried cost: what the cap path has not recorded when a run enters reserve mode is recorded FIRST, so it never shares the
// run's grant; and when it cannot be recorded the run is refused, with nothing reserved.
func TestCarriedCostIsRecordedBeforeTheGrantAndNeverSharesIt(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 1})
	sx := &scripted{steps: []step{{final: 0.1}}}
	c := scriptedConductor(r, sx)
	w := directWorker(r, "w-1")
	r.approve()
	w.cost = 0.6 // an earlier run's cost that was never recorded
	r.runOnce(c, w, tbg)
	rows := r.resvRows()
	if len(rows) != 1 || !spendNear(rows[0].amount, 0.4) || sx.budgets[0] > 0.4+1e-9 {
		t.Fatalf("the grant is what is left AFTER the carried 0.6: %+v budgets %v", rows, sx.budgets)
	}
	if got := r.session().SpentUSD; !spendNear(got, 0.7) {
		t.Fatalf("spent %v, want the carried 0.6 plus the run's 0.1", got)
	}

	w2 := directWorker(r, "w-2")
	w2.cost = 0.2
	if _, err := r.db.Exec(`CREATE TRIGGER block_spend BEFORE UPDATE OF spent_usd ON task_sessions BEGIN SELECT RAISE(ABORT, 'database is busy'); END`); err != nil {
		t.Fatal(err)
	}
	before := len(r.resvRows())
	if _, err := c.execOnce(tbg, w2, opus, "doing task", ""); err == nil || !strings.Contains(err.Error(), "budget") || sx.runs() != 1 {
		t.Fatalf("refused while the carried cost cannot be recorded: %v (runs %d)", err, sx.runs())
	}
	if len(r.resvRows()) != before {
		t.Fatal("nothing is reserved on top of unrecorded cost")
	}
}

// The grant: the turn's remainder when it has one, else min(left, $1); the run is capped at it.
func TestWorkerReservationGrantAndCap(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	sx := &scripted{steps: []step{{final: 0.1}, {final: 0.1}}}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 5, MaxConcurrent: 2, BudgetUSD: 3}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: sx.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	t.Cleanup(c.Close)
	w, w2 := directWorker(r, "w-1"), directWorker(r, "w-2")
	r.approve()
	r.runOnce(c, w, tbg)
	if sx.budgets[0] != 3 {
		t.Fatalf("a turn limit of 3 is the ask: %v", sx.budgets)
	}
	c2 := r.clockedConductor(Limits{})
	c2.cfg.Exec = sx.exec
	r.runOnce(c2, w2, tbg)
	if sx.budgets[1] != missionExecAskUSD {
		t.Fatalf("no turn limit: min(left, $1) = 1, got %v", sx.budgets)
	}
	if rows := r.resvRows(); rows[0].amount != 3 || rows[1].amount != 1 {
		t.Fatalf("%+v", rows)
	}
}

// A grant under $0.01 is the same refusal as a spent budget, before the runtime runs.
func TestWorkerReservationRefusedBelowAPenny(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 0.005})
	sx := &scripted{steps: []step{{final: 0.1}}}
	c := scriptedConductor(r, sx)
	w := directWorker(r, "w-1")
	r.approve()
	_, err := c.execOnce(tbg, w, opus, "doing task", "")
	if err == nil || !strings.Contains(err.Error(), "budget") || sx.runs() != 0 {
		t.Fatalf("refused before the run: %v (runs %d)", err, sx.runs())
	}
	if len(r.resvRows()) != 0 || r.capRuns() != 0 {
		t.Fatal("nothing reserved or registered")
	}
}

// With no hook nothing registers (the cap as before); with the hook saying off a run registers on the cap for as long as
// it runs; an unreadable setting is off, with one notice.
func TestWorkerAccountingOffKeepsTheCapPath(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	if len(r.resvRows()) != 0 || r.capRuns() != 0 {
		t.Fatalf("no hook: nothing is written (%d reservations, %d cap runs)", len(r.resvRows()), r.capRuns())
	}
	if got := r.session().SpentUSD; !spendNear(got, 0.01) {
		t.Fatalf("spent %v", got)
	}
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return false, fmt.Errorf("db is gone") }
	b := r.add(spec{title: "b", files: "b.go"})
	r.next(r.c)
	eventually(t, "b is in flight", func() bool { return r.capRuns() == 1 })
	r.ex.release(b.ID)
	r.waitStatus(b.ID, tasks.StatusReview)
	eventually(t, "the registration ends with the run", func() bool { return r.capRuns() == 0 })
	if n := len(r.resvRows()); n != 0 || countNotices(r.em, "mission.reservations_setting") != 1 {
		t.Fatalf("%d reservations, %d notices", n, countNotices(r.em, "mission.reservations_setting"))
	}
}

// A start that fails before the runtime runs gives its money back; a cap registration is removed.
func TestWorkerReservationReleasedWhenTheStartFails(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	c := scriptedConductor(r, &scripted{})
	w := directWorker(r, "w-1")
	r.approve()
	a, err := c.enterRun(tbg, w, &monomind.ExecOptions{})
	if err != nil || a == nil || a.mode != modeReserve {
		t.Fatal(a, err)
	}
	c.endRun(w, a) // the start failed: ran is false
	if rows := r.resvRows(); len(rows) != 1 || rows[0].state != tasks.ReservationReleased {
		t.Fatalf("%+v", rows)
	}
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return false, nil }
	a, err = c.enterRun(tbg, w, &monomind.ExecOptions{})
	if err != nil || a == nil || a.mode != modeCap || r.capRuns() != 1 {
		t.Fatal(a, err)
	}
	c.endRun(w, a)
	if r.capRuns() != 0 {
		t.Fatal("the registration outlived the run")
	}
}

// A Conductor's owner id is 128 random bits, one per Conductor, and not the process id.
func TestConductorOwnerIDIsRandomAndDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 1, MaxConcurrent: 1}})
		if len(c.ownerID) != 32 || seen[c.ownerID] || strings.Contains(c.ownerID, fmt.Sprint(os.Getpid())) && len(fmt.Sprint(os.Getpid())) > 4 {
			t.Fatalf("owner id %q", c.ownerID)
		}
		seen[c.ownerID] = true
		c.Close()
	}
}

// A reservation the store no longer holds for the Conductor stops that worker and the notice carries the cost.
func TestWorkerReservationLostStopsTheWorkerAndNamesTheCost(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10, lease: 24 * time.Hour})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.onRun = func(ctx context.Context, id int64, on func(monomind.Event)) { on(usageCost(0.4)) }
	r.next(r.c)
	eventually(t, "the charge", func() bool { return spendNear(r.session().SpentUSD, 0.4) })
	// A suspend: the lease lapses and is swept. A tick that began before the advance may still renew the lapsed row once
	// (by design), so the advance and the sweep repeat until the loss is seen.
	eventually(t, "the notice", func() bool {
		r.clk.advance(tasks.ReservationLease + tasks.ReservationGrace + time.Second)
		if _, err := r.store.ExpireStale(tbg, "default", r.sid); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		return countNotices(r.em, "mission.reservation_lost") >= 1
	})
	b, _ := json.Marshal(r.em.find("notice"))
	if !strings.Contains(string(b), "0.400000") || !strings.Contains(string(b), "task session reservation resolve") {
		t.Fatalf("the notice names the number and the way out: %s", b)
	}
	eventually(t, "the worker to stop", func() bool { return atomic.LoadInt32(&r.ex.running) == 0 && r.c.taskWorker(a.ID) == nil })
	rows := r.resvRows()
	if len(rows) != 1 || rows[0].state != tasks.ReservationExpired || !spendNear(rows[0].charge, 0.4) {
		t.Fatalf("%+v", rows)
	}
	if got := r.session().SpentUSD; !spendNear(got, 0.4) {
		t.Fatalf("spent %v: recorded once, not again at the stop", got)
	}
}

// The heartbeat rides the tick a third of the lease apart, on its own clock.
func TestWorkerReservationHeartbeatCadence(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10, lease: 24 * time.Hour})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "the reservation", func() bool { return len(r.resvRows()) == 1 })
	first := r.resvRows()[0].hb
	r.clk.advance(tasks.ReservationLease/3 - 10*time.Second)
	time.Sleep(200 * time.Millisecond) // many ticks (10ms apart)
	if hb := r.resvRows()[0].hb; hb != first {
		t.Fatalf("renewed too early: %s -> %s", first, hb)
	}
	r.clk.advance(20 * time.Second)
	eventually(t, "the renewal", func() bool { return r.resvRows()[0].hb != first })
	if rows := r.resvRows(); rows[0].state != tasks.ReservationLive {
		t.Fatalf("%+v", rows)
	}
}

// Twenty workers over one session: no deadlock, every cost recorded once, every reservation settled.
func TestWorkerReservationsTwentyWorkers(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 100})
	sx := &scripted{}
	for i := 0; i < 20; i++ {
		sx.steps = append(sx.steps, step{stream: []float64{0.1, 0.2}, final: 0.3})
	}
	c := scriptedConductor(r, sx)
	var ws []*worker
	for i := 0; i < 20; i++ {
		ws = append(ws, directWorker(r, fmt.Sprintf("w-%d", i)))
	}
	r.approve()
	var wg sync.WaitGroup
	done := make(chan struct{})
	for _, w := range ws {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.execOnce(tbg, w, opus, "doing task", "")
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("deadlock or unbounded wait")
	}
	if got := r.session().SpentUSD; !spendNear(got, 6) {
		t.Fatalf("spent %v, want 6", got)
	}
	for _, x := range r.resvRows() {
		if x.state != tasks.ReservationSettled || !spendNear(x.charge, 0.3) {
			t.Fatalf("%+v", x)
		}
	}
}

// A run that reported no cost at all is charged its whole grant (nothing says it cost less); one that reported a zero is not.
func TestRunWithoutAnyCostReportIsChargedItsGrant(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	sx := &scripted{steps: []step{{noCost: true}, {final: 0}}}
	c := scriptedConductor(r, sx)
	w := directWorker(r, "w-1")
	r.approve()
	r.runOnce(c, w, tbg)
	if got := r.session().SpentUSD; !spendNear(got, missionExecAskUSD) {
		t.Fatalf("a run that never said: spent %v, want its grant %v", got, missionExecAskUSD)
	}
	r.runOnce(c, w, tbg)
	if got := r.session().SpentUSD; !spendNear(got, missionExecAskUSD) {
		t.Fatalf("a reported zero is a zero: spent %v", got)
	}
}

// The latch, reserve side: a reserving run in flight keeps the next run on reservations whatever the setting says now
// (off, or unreadable), so B finds A's dollar held and is refused; once A is over the flip applies.
func TestFlipOffWhileAReservingRunIsInFlightDoesNotMixModes(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 1})
	var mode atomic.Int32 // 1 on, 0 off, 2 unreadable
	mode.Store(1)
	r.mc.WorkerReservations = func(context.Context) (bool, error) {
		switch mode.Load() {
		case 1:
			return true, nil
		case 2:
			return false, fmt.Errorf("db busy")
		}
		return false, nil
	}
	sx := &scripted{steps: []step{{final: 0.1}, {final: 0.1}}}
	c := scriptedConductor(r, sx)
	a, b := directWorker(r, "w-a"), directWorker(r, "w-b")
	r.approve()
	held, err := c.enterRun(tbg, a, &monomind.ExecOptions{}) // A holds the whole $1
	if err != nil || held.mode != modeReserve {
		t.Fatal(held, err)
	}
	for _, m := range []int32{0, 2} {
		mode.Store(m)
		if _, err := c.execOnce(tbg, b, opus, "doing task", ""); err == nil || !strings.Contains(err.Error(), "budget") || sx.runs() != 0 || r.capRuns() != 0 {
			t.Fatalf("setting %d: B must get nothing while A holds the dollar, and never run beside it on the cap: %v (runs %d, cap runs %d)", m, err, sx.runs(), r.capRuns())
		}
	}
	held.ran, held.finalOK = true, true // it ran and reported a zero
	c.endRun(a, held)
	r.runOnce(c, b, tbg) // drained: the setting (off) applies now
	if r.capRuns() != 0 || len(r.resvRows()) != 1 {
		t.Fatalf("a cap run ended: %d registrations, %d reservations", r.capRuns(), len(r.resvRows()))
	}
}

// The latch, cap side, across Conductors: a run in flight on the cap keeps every reservation off the session, so
// switching ON mid-run lets nobody take the cap's dollar; the other run follows the cap until the drain.
func TestFlipOnWhileACapRunIsInFlightDoesNotMixModes(t *testing.T) {
	r := newRig(t, rigOpts{budget: 1})
	var on atomic.Bool
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return on.Load(), nil }
	sx := &scripted{steps: []step{{stream: []float64{0.1}, block: true}, {final: 0.1}, {final: 0.1}}, started: make(chan struct{}, 4)}
	c, c2 := scriptedConductor(r, sx), scriptedConductor(r, sx) // two "processes" on one profile
	a, b := directWorker(r, "w-a"), directWorker(r, "w-b")
	r.approve()
	ctx, cancel := context.WithCancel(tbg)
	done := make(chan struct{})
	go func() { _, _ = c.execOnce(ctx, a, opus, "doing task", ""); close(done) }()
	<-sx.started
	on.Store(true)
	r.runOnce(c2, b, tbg) // B follows the cap: no reservation while A is in flight
	if len(r.resvRows()) != 0 {
		t.Fatalf("B reserved beside a run on the cap: %+v", r.resvRows())
	}
	if _, err := r.store.ReserveExec(tbg, "default", r.sid, "x", "w-x", "e1", 0.5); err == nil {
		t.Fatal("the store itself refuses a reservation beside a cap run")
	}
	cancel()
	<-done
	r.runOnce(c2, b, tbg) // drained: the switch applies
	if rows := r.resvRows(); len(rows) != 1 {
		t.Fatalf("%+v", rows)
	}
}

// A heartbeat of run 1 that answers after run 1 ended and run 2 began changes nothing: it must neither stop the new run
// nor name the wrong row.
func TestStaleHandleLossIsIgnored(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	c := scriptedConductor(r, &scripted{})
	w := directWorker(r, "w-1")
	r.approve()
	a1, err := c.enterRun(tbg, w, &monomind.ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a1.ran = true
	c.endRun(w, a1)
	a2, err := c.enterRun(tbg, w, &monomind.ExecOptions{}) // the fallback run
	if err != nil {
		t.Fatal(err)
	}
	c.accountingLost(w, a1) // the delayed heartbeat of run 1 answers
	if w.task.stopped != "" || w.task.acct != a2 || a2.lost || countNotices(r.em, "mission.reservation_lost") != 0 {
		t.Fatalf("the new run is untouched: stopped=%q lost=%v notices=%d", w.task.stopped, a2.lost, countNotices(r.em, "mission.reservation_lost"))
	}
	c.endRun(w, a2)
}

// A cap run that lapsed and finds a reservation on the session loses its place and is stopped; one with nothing else on
// the session is renewed.
func TestCapRunLostToAReservationStopsTheWorker(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return false, nil }
	c := scriptedConductor(r, &scripted{})
	w := directWorker(r, "w-1")
	r.approve()
	a, err := c.enterRun(tbg, w, &monomind.ExecOptions{})
	if err != nil || a.mode != modeCap {
		t.Fatal(a, err)
	}
	w.status = chatevents.AgentWorking
	r.clk.advance(tasks.ReservationLease + tasks.ReservationGrace + time.Second)
	if _, err := r.store.ReserveExec(tbg, "default", r.sid, "x", "w-x", "e1", 2); err != nil { // lapsed: ignored by the latch
		t.Fatal(err)
	}
	c.heartbeat(tbg, r.mc, w, r.clk.now())
	if !a.lost || w.task.stopped == "" || countNotices(r.em, "mission.accounting_lost") != 1 {
		t.Fatalf("lost=%v stopped=%q notices=%d", a.lost, w.task.stopped, countNotices(r.em, "mission.accounting_lost"))
	}
}

// A settle that fails for good tells the operator the reservation id and the way out, also at zero cost.
func TestWorkerReservationFailedSettleAlwaysNotices(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	if _, err := r.db.Exec(`CREATE TRIGGER block_settle BEFORE UPDATE ON task_session_reservations WHEN NEW.state = 'settled'
		BEGIN SELECT RAISE(ABORT, 'database is busy'); END`); err != nil {
		t.Fatal(err)
	}
	sx := &scripted{steps: []step{{final: 0}}}
	c := scriptedConductor(r, sx)
	w := directWorker(r, "w-1")
	r.approve()
	r.runOnce(c, w, tbg)
	b, _ := json.Marshal(r.em.find("notice"))
	if countNotices(r.em, "mission.reservation_lost") != 1 || !strings.Contains(string(b), "reservation resolve 1 --usd 0.000000") || !strings.Contains(string(b), fmt.Sprintf("session #%d", r.sid)) {
		t.Fatalf("no recovery notice: %s", b)
	}
}

// A hold abandoned by a dead Conductor is swept by a fresh one at its first tick, though it never reserved.
func TestWorkerReservationFreshConductorSweepsAnAbandonedHold(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 1})
	if _, err := r.store.Reserve(tbg, "default", r.sid, "dead-owner", "w-9", 1); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(tasks.ReservationLease + tasks.ReservationGrace + time.Second)
	r.c.missionWatch(r.sid)
	eventually(t, "the sweep", func() bool { rows := r.resvRows(); return len(rows) == 1 && rows[0].state == tasks.ReservationExpired })
}
