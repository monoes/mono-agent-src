package dynorg

import (
	"context"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
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

func (r *missionRig) capRuns() int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM task_session_cap_runs`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// newResvRig is a rig whose workers start in reserve mode (the setting on) and whose conductor runs on the rig's clock.
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
	stream  []float64
	final   float64
	noCost  bool  // the run's result reports no cost at all
	noStart bool  // no start event is emitted
	notDone bool  // the result says the turn never finished (no done event) and carries no error
	resErr  bool  // the result carries a protocol error (a failure that came back as a result)
	err     error // Exec fails with this (no result) after its events
	block   bool  // wait for the context instead of ending
}

type scripted struct {
	mu      sync.Mutex
	steps   []step
	n       int
	budgets []float64
	started chan struct{}
	// onRun, when set, runs inside every run (the run is in flight).
	onRun func()
}

func (s *scripted) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	s.mu.Lock()
	st := step{}
	if s.n < len(s.steps) {
		st = s.steps[s.n]
	}
	s.n++
	s.budgets = append(s.budgets, o.BudgetUSD)
	s.mu.Unlock()
	if !st.noStart {
		on(monomind.Event{Type: monomind.EventStart})
	}
	for _, c := range st.stream {
		on(usageCost(c))
	}
	if s.onRun != nil {
		s.onRun()
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
	if st.err != nil {
		return nil, st.err
	}
	if st.block && st.noStart {
		return nil, ctx.Err()
	}
	res := &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, CostUSD: st.final, HasCostUSD: !st.noCost}
	if st.notDone {
		res.SawDone = false
	}
	if st.resErr {
		res.Err = &monomind.ProtocolError{Code: "auth", Message: "auth failed", Fatal: true}
	}
	return res, nil
}

func (s *scripted) runs() int { s.mu.Lock(); defer s.mu.Unlock(); return s.n }

func scriptedConductor(r *missionRig, sx *scripted) *Conductor {
	c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 40, MaxConcurrent: 30}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: sx.exec, Emit: r.em, Missions: r.mc, Now: r.clk.now})
	r.t.Cleanup(c.Close)
	return c
}

// directWorker is a mission worker on a task, run by hand (execOnce) so that one worker has several runs.
func directWorker(r *missionRig, id string) *worker {
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
	c.recordSpend(ctx, r.mc, w) // what missionFinish does when a run ends
}
