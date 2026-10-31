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

type resvRow struct {
	id             int64
	ref, owner     string
	state          string
	amount, charge float64
	hb             string
}

func (r *missionRig) resvRows() []resvRow {
	r.t.Helper()
	rows, err := r.db.Query(`SELECT id, worker_ref, owner_id, state, amount_usd, charged_usd, heartbeat_at FROM task_session_reservations ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []resvRow
	for rows.Next() {
		var x resvRow
		if err := rows.Scan(&x.id, &x.ref, &x.owner, &x.state, &x.amount, &x.charge, &x.hb); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, x)
	}
	return out
}

// newResvRig is a rig whose workers reserve (the setting on) and whose conductor runs on the rig's clock.
func newResvRig(t *testing.T, o rigOpts) *missionRig {
	t.Helper()
	r := newRig(t, o)
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return true, nil }
	r.c = r.clockedConductor(o.lim)
	return r
}

func (r *missionRig) clockedConductor(lim Limits) *Conductor {
	if lim.MaxAgents == 0 {
		lim = Limits{MaxAgents: 20, MaxConcurrent: 5}
	}
	c := New(tbg, Config{Cwd: "/w", Limits: lim, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	r.t.Cleanup(c.Close)
	return c
}

// step is one scripted run: it streams the cumulative costs, then (unless block) ends with the final cost.
type step struct {
	stream []float64
	final  float64
	block  bool // wait for the context instead of ending
}

type scripted struct {
	mu      sync.Mutex
	steps   []step
	n       int
	budgets []float64
	started chan struct{}
}

func (s *scripted) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	s.mu.Lock()
	st := s.steps[s.n]
	s.n++
	s.budgets = append(s.budgets, o.BudgetUSD)
	s.mu.Unlock()
	on(monomind.Event{Type: monomind.EventStart})
	for _, c := range st.stream {
		on(usageCost(c))
	}
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if st.block {
		<-ctx.Done()
	}
	return &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, CostUSD: st.final, HasCostUSD: true}, nil
}

// directWorker is a mission worker on a task, run by hand (execOnce) so that one worker has several runs.
func directWorker(r *missionRig, c *Conductor, id string) *worker {
	tk := r.add(spec{title: id, files: id + ".go"})
	name := missionWorkerName(r.sid, tk.ID)
	return &worker{id: id, staff: Staff{Role: "dev", Access: ProfileCoding}, model: opus, status: chatevents.AgentWorking,
		task: &missionBind{session: r.sid, task: tk, name: name, actor: childActor(r.sid, tk.ID, name), executor: ExecWorker}}
}

func (r *missionRig) runOnce(c *Conductor, w *worker, ctx context.Context) {
	r.t.Helper()
	if _, err := c.execOnce(ctx, w, opus, "doing task", ""); err != nil {
		r.t.Fatal(err)
	}
}

// THE MUST-PASS TEST (R1). A worker's cost is a running total over all its runs, a reservation is per run:
// across several runs, a fallback model, a stop in the middle of a run and a settle after partial charges,
// the session is charged each cost once, and every reservation's charge is its own run's cost.
func TestWorkerReservationBaselineAcrossRuns(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	sx := &scripted{steps: []step{
		{stream: []float64{0.1, 0.4}, final: 0.5},    // run 1: partial charges, settle adds the rest
		{stream: []float64{0.3}, final: 0.3},         // run 2 (the fallback model): streamed = final
		{final: 0.2},                                 // run 3: nothing streamed, all at settle
		{stream: []float64{0.15, 0.25}, block: true}, // run 4: stopped mid-run; the runtime still reports 0.25 at the end
		{stream: []float64{0.05}, final: 0.05},       // run 5, after the stop
	}}
	sx.steps[3].final = 0.25
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 5, MaxConcurrent: 2}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: sx.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	t.Cleanup(c.Close)
	w := directWorker(r, c, "w-1")
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
		x := rows[i]
		if x.state != tasks.ReservationSettled || !spendNear(x.charge, cost) || x.ref != fmt.Sprintf("w-1#%d", i+1) {
			t.Errorf("reservation %d = %+v, want settled at %v", i+1, x, cost)
		}
	}
	if w.task.resv != nil {
		t.Fatal("nothing is left live")
	}
}

// A run killed after its charges and before its settle leaves the charges in the spend and the rest held; the
// operator's resolve then charges only the difference.
func TestWorkerReservationKilledAfterChargeBeforeSettle(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	sx := &scripted{steps: []step{{stream: []float64{0.4}, block: true}}, started: make(chan struct{}, 1)}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 5, MaxConcurrent: 2}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: sx.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	w := directWorker(r, c, "w-1")
	r.approve()
	go func() { _, _ = c.execOnce(tbg, w, opus, "doing task", "") }() // never settled in this test: the process "dies" here
	<-sx.started
	eventually(t, "the charge", func() bool { return spendNear(r.session().SpentUSD, 0.4) })
	rows := r.resvRows()
	if len(rows) != 1 || rows[0].state != tasks.ReservationLive || !spendNear(rows[0].charge, 0.4) || rows[0].amount != 1 {
		t.Fatalf("%+v", rows)
	}
	r.clk.advance(tasks.ReservationLease + tasks.ReservationGrace + time.Second)
	if n, err := r.store.ExpireStale(tbg, "default", r.sid); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if err := r.store.ResolveReservation(tbg, "default", rows[0].id, 0.7, "test", operator); err != nil {
		t.Fatal(err)
	}
	if got := r.session().SpentUSD; !spendNear(got, 0.7) {
		t.Fatalf("spent %v: the 0.4 charged on the way is not charged again", got)
	}
	c.cancel() // let the stranded goroutine end
}

// The grant: the turn's remainder when it has one, else min(left, $1); the run is capped at it.
func TestWorkerReservationGrantAndCap(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	sx := &scripted{steps: []step{{final: 0.1}, {final: 0.1}}}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 5, MaxConcurrent: 2, BudgetUSD: 3}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: sx.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	t.Cleanup(c.Close)
	w := directWorker(r, c, "w-1")
	w2 := directWorker(r, c, "w-2")
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

// A grant under $0.01 is the same refusal as a spent budget.
func TestWorkerReservationRefusedBelowAPenny(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 0.005})
	sx := &scripted{steps: []step{{final: 0.1}}}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 5, MaxConcurrent: 2}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: sx.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	t.Cleanup(c.Close)
	w := directWorker(r, c, "w-1")
	r.approve()
	_, err := c.execOnce(tbg, w, opus, "doing task", "")
	if err == nil || !strings.Contains(err.Error(), "budget") || sx.n != 0 {
		t.Fatalf("refused before the run: %v (runs %d)", err, sx.n)
	}
	if len(r.resvRows()) != 0 {
		t.Fatal("nothing reserved")
	}
}

// With the setting off nothing reserves and the old cap applies.
func TestWorkerReservationsOffKeepTheOldPath(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	if n := len(r.resvRows()); n != 0 {
		t.Fatalf("%d reservations with the setting off", n)
	}
	if got := r.session().SpentUSD; !spendNear(got, 0.01) {
		t.Fatalf("spent %v", got)
	}
	// an unreadable setting is off, with one notice
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return false, fmt.Errorf("db is gone") }
	b := r.add(spec{title: "b", files: "b.go"})
	r.next(r.c)
	r.ex.release(b.ID)
	r.waitStatus(b.ID, tasks.StatusReview)
	if n := len(r.resvRows()); n != 0 || countNotices(r.em, "mission.reservations_setting") != 1 {
		t.Fatalf("%d reservations, %d notices", n, countNotices(r.em, "mission.reservations_setting"))
	}
}

// A start that fails before the runtime runs gives its money back.
func TestWorkerReservationReleasedWhenTheStartFails(t *testing.T) {
	r := newResvRig(t, rigOpts{budget: 10})
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 5, MaxConcurrent: 2}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: nil, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	t.Cleanup(c.Close)
	w := directWorker(r, c, "w-1")
	r.approve()
	h, err := c.reserveExec(tbg, w, &monomind.ExecOptions{})
	if err != nil || h == nil {
		t.Fatal(err)
	}
	c.endExecReservation(w, h) // the start failed: ran is false
	if rows := r.resvRows(); len(rows) != 1 || rows[0].state != tasks.ReservationReleased {
		t.Fatalf("%+v", rows)
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
	r.clk.advance(tasks.ReservationLease + tasks.ReservationGrace + time.Second) // a suspend: the lease lapsed and was swept
	eventually(t, "the notice", func() bool { return countNotices(r.em, "mission.reservation_lost") >= 1 })
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
	ex := func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		on(monomind.Event{Type: monomind.EventStart})
		on(usageCost(0.1))
		on(usageCost(0.2))
		return &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, CostUSD: 0.3, HasCostUSD: true}, nil
	}
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 30, MaxConcurrent: 20}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: ex, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	t.Cleanup(c.Close)
	var ws []*worker
	for i := 0; i < 20; i++ {
		ws = append(ws, directWorker(r, c, fmt.Sprintf("w-%d", i)))
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
