package dynorg

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// Seeds and sizes are fixed. A failure names its seed; the choices (who flips when, which worker runs on which
// Conductor) replay, the interleaving is the scheduler's.
var accountRaceSeeds = []int64{11, 12, 13, 14}

const accountWorkers, accountRuns, accountConductors = 8, 6, 3

// Workers on three Conductors ("processes" of one profile) run many runs over one session while the operator's switch is
// flipped at random, so runs start in both modes and a flip lands in the middle of runs. Held at every instant a run is
// in flight: the session is accounted in ONE mode (read in one statement), and at the end every run's cost is in the
// session's spend exactly once (no lost and no duplicated charge) with nothing held or registered.
func TestWorkersRaceInBothModesWhileTheSwitchIsFlipped(t *testing.T) {
	for _, seed := range accountRaceSeeds {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { accountingRace(t, seed) })
	}
}

func accountingRace(t *testing.T, seed int64) {
	r := newRig(t, rigOpts{budget: 1000})
	var reserve atomic.Bool
	r.mc.WorkerReservations = func(context.Context) (bool, error) { return reserve.Load(), nil }
	var mixed, empty atomic.Int64
	ex := func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		on(monomind.Event{Type: monomind.EventStart})
		on(usageCost(0.1))
		var capN, resN int
		if err := r.db.QueryRow(`SELECT (SELECT COUNT(*) FROM task_session_cap_runs), (SELECT COUNT(*) FROM task_session_reservations WHERE state = 'live')`).Scan(&capN, &resN); err != nil {
			t.Error(err)
		}
		switch {
		case capN > 0 && resN > 0:
			mixed.Add(1)
		case capN == 0 && resN == 0:
			empty.Add(1) // a run in flight is registered one way or the other
		}
		on(usageCost(0.2))
		return &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, CostUSD: 0.3, HasCostUSD: true}, nil
	}
	var cs []*Conductor
	for i := 0; i < accountConductors; i++ {
		c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 40, MaxConcurrent: 30}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
			Exec: ex, Emit: r.em, Missions: r.mc, Now: r.clk.now})
		t.Cleanup(c.Close)
		cs = append(cs, c)
	}
	var ws []*worker
	for i := 0; i < accountWorkers; i++ {
		ws = append(ws, directWorker(r, fmt.Sprintf("w%d", i)))
	}
	r.approve()
	stop := make(chan struct{})
	var flipper sync.WaitGroup
	flipper.Add(1)
	go func() {
		defer flipper.Done()
		fr := rand.New(rand.NewSource(seed))
		for {
			select {
			case <-stop:
				return
			default:
				reserve.Store(fr.Intn(2) == 0)
				time.Sleep(50 * time.Microsecond) // a flip, not a spin: a busy loop starves the writers of CPU
			}
		}
	}()
	var wg sync.WaitGroup
	var ok atomic.Int64
	for i, w := range ws {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := cs[i%accountConductors]
			for n := 0; n < accountRuns; {
				_, err := c.execOnce(tbg, w, opus, "doing task", "")
				switch {
				case err == nil:
					c.recordSpend(tbg, r.mc, w) // what missionFinish does
					ok.Add(1)
					n++
				case errors.Is(err, errBudgetRefused): // the session is changing mode: the scheduler retries
				default:
					t.Errorf("run: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	flipper.Wait()
	if mixed.Load() > 0 || empty.Load() > 0 {
		t.Fatalf("seed %d: %d instants with both modes live, %d runs in flight in neither", seed, mixed.Load(), empty.Load())
	}
	want := 0.3 * float64(ok.Load())
	if got := r.session().SpentUSD; !spendNear(got, want) {
		t.Fatalf("seed %d: spent %v for %d runs of 0.3 (%v): a charge was lost or taken twice", seed, got, ok.Load(), want)
	}
	if r.capRuns() != 0 {
		t.Fatalf("seed %d: cap runs left registered", seed)
	}
	for _, x := range r.resvRows() {
		if x.state != "settled" {
			t.Fatalf("seed %d: %+v", seed, x)
		}
	}
}

// The last dollars, in reserve mode: workers on three Conductors each ask for $1 of a $3 session and spend all of it.
// However they interleave the session is never committed past its budget, exactly the runs that were granted ran, and
// what they report is what the session spent.
func TestWorkersRaceForTheLastDollarInReserveMode(t *testing.T) {
	for _, seed := range accountRaceSeeds {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			r := newRig(t, rigOpts{budget: 3})
			r.mc.WorkerReservations = func(context.Context) (bool, error) { return true, nil }
			var over atomic.Int64
			ex := func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
				on(monomind.Event{Type: monomind.EventStart})
				if c, err := r.store.Commitment(tbg, "default", r.sid); err != nil || c.Total() > 3+1e-9 {
					over.Add(1)
				}
				on(usageCost(o.BudgetUSD))
				return &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, CostUSD: o.BudgetUSD, HasCostUSD: true}, nil
			}
			var cs []*Conductor
			for i := 0; i < accountConductors; i++ {
				c := New(tbg, Config{Cwd: "/w", Limits: Limits{MaxAgents: 40, MaxConcurrent: 30}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
					Exec: ex, Emit: r.em, Missions: r.mc, Now: r.clk.now})
				t.Cleanup(c.Close)
				cs = append(cs, c)
			}
			var ws []*worker
			for i := 0; i < accountWorkers; i++ {
				ws = append(ws, directWorker(r, fmt.Sprintf("w%d", i)))
			}
			r.approve()
			rnd := rand.New(rand.NewSource(seed))
			var wg sync.WaitGroup
			var granted atomic.Int64
			for _, w := range ws {
				c := cs[rnd.Intn(accountConductors)]
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := c.execOnce(tbg, w, opus, "doing task", ""); err == nil {
						granted.Add(1)
					} else if !errors.Is(err, errBudgetRefused) {
						t.Errorf("run: %v", err)
					}
				}()
			}
			wg.Wait()
			if over.Load() > 0 {
				t.Fatalf("seed %d: the session was committed past its budget %d times", seed, over.Load())
			}
			if got := r.session().SpentUSD; granted.Load() != 3 || !spendNear(got, 3) {
				t.Fatalf("seed %d: %d runs were granted, spent %v: want exactly 3 runs of $1", seed, granted.Load(), got)
			}
		})
	}
}
