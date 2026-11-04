package tasks

import (
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

// raceSeeds are fixed: a failure names its seed and reruns the same choices (the interleaving is the scheduler's).
var raceSeeds = []int64{1, 2, 3, 4, 5}

const raceProcs, raceOps = 6, 40

// Workers in separate "processes" race for the last dollars of one session in BOTH modes while a switch is flipped
// at random. A run follows the mode that is in flight, so:
//   - cap runs and reservations are never live on the session together (read in one statement, so one instant),
//   - the reservations never hold or spend more than the budget,
//   - every dollar a reservation run reports is in the spend once, and nothing is held at the end.
func TestWorkersRaceForTheLastDollarInBothModesWithTheSwitchFlipped(t *testing.T) {
	for _, seed := range raceSeeds {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) { runModeRace(t, seed) })
	}
}

func runModeRace(t *testing.T, seed int64) {
	stores := processes(t, raceProcs)
	const budget = 6.0
	session := seedBudget(t, stores[0].db, budget)
	var reserveMode atomic.Bool // the operator's switch
	var charged, mixed, refused atomic.Int64
	check := func(s *Store, where string) {
		var capN, resN int
		if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM task_session_cap_runs WHERE session_id = ?),
			(SELECT COUNT(*) FROM task_session_reservations WHERE session_id = ? AND state = 'live')`, session, session).Scan(&capN, &resN); err != nil {
			t.Errorf("%s: %v", where, err)
			return
		}
		if capN > 0 && resN > 0 {
			mixed.Add(1)
		}
		if c, err := s.Commitment(bg, "default", session); err == nil && c.Total() > budget+1e-9 {
			t.Errorf("%s: committed %v of %v", where, c.Total(), budget)
		}
	}
	stop := make(chan struct{})
	var flipper sync.WaitGroup
	flipper.Add(1)
	go func() {
		defer flipper.Done()
		r := rand.New(rand.NewSource(seed))
		for {
			select {
			case <-stop:
				return
			default:
				reserveMode.Store(r.Intn(2) == 0)
			}
		}
	}()
	var wg sync.WaitGroup
	for p := 0; p < raceProcs; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, owner, r := stores[p], fmt.Sprintf("proc-%d-%d", seed, p), rand.New(rand.NewSource(seed*100+int64(p)))
			for i := 0; i < raceOps; i++ {
				exec := fmt.Sprintf("e%d", i)
				tryReserve := func() bool {
					rs, err := s.ReserveExecAsk(bg, "default", session, owner, owner, exec, 0, 1.5)
					switch {
					case err == nil:
						check(s, "reserved")
						cost := float64(r.Intn(int(rs.AmountUSD*100)+1)) / 100
						if err := s.ChargeExec(bg, "default", rs.ID, owner, cost, true); err != nil {
							t.Errorf("settle: %v", err)
							return true
						}
						charged.Add(toMicros(cost))
						return true
					case errors.Is(err, ErrAccountingMode):
						return false
					case errors.Is(err, ErrLimit):
						refused.Add(1)
						return true // no money left: the run is refused
					default:
						t.Errorf("reserve: %v", err)
						return true
					}
				}
				tryCap := func() bool {
					run, err := s.BeginCapRun(bg, "default", session, owner, exec)
					if err != nil {
						if !errors.Is(err, ErrAccountingMode) {
							t.Errorf("begin: %v", err)
							return true
						}
						return false
					}
					check(s, "cap run")
					if err := s.EndCapRun(bg, "default", run.ID, owner); err != nil {
						t.Errorf("end: %v", err)
					}
					return true
				}
				first, second := tryCap, tryReserve
				if reserveMode.Load() {
					first, second = tryReserve, tryCap
				}
				if !first() {
					second() // follows the mode in flight; a miss on both is a refused start, nothing is taken
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	flipper.Wait()
	if mixed.Load() > 0 {
		t.Fatalf("seed %d: the two modes were live on one session together %d times", seed, mixed.Load())
	}
	c, err := stores[0].Commitment(bg, "default", session)
	if err != nil {
		t.Fatal(err)
	}
	if c.Held != 0 || !near(c.Spent, fromMicros(charged.Load())) || c.Total() > budget+1e-9 {
		t.Fatalf("seed %d: %+v, reported %v", seed, c, fromMicros(charged.Load()))
	}
	if capRows(t, stores[0], session) != 0 {
		t.Fatalf("seed %d: cap runs left registered", seed)
	}
}

// No double grant, per path, between two processes on one session, seeded start order, 60 rounds each (a fresh session
// per round): a cap run against a reservation, and a reservation against a reservation for the last dollar. Exactly one
// of the two wins every round; the loser is refused (ErrAccountingMode, ErrLimit) and holds nothing.
func TestTwoProcessesNeverBothGetTheSameDollarOnAnyPath(t *testing.T) {
	stores := processes(t, 2)
	rnd := rand.New(rand.NewSource(77))
	for round := 0; round < 60; round++ {
		for _, path := range []string{"cap-vs-reserve", "reserve-vs-reserve"} {
			session := seedBudget(t, stores[0].db, 1)
			first := rnd.Intn(2)
			var wins atomic.Int32
			var wg sync.WaitGroup
			for p := 0; p < 2; p++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s, owner := stores[(p+first)%2], fmt.Sprintf("p%d", p)
					var err error
					if path == "cap-vs-reserve" && p == 0 {
						_, err = s.BeginCapRun(bg, "default", session, owner, "r")
					} else {
						_, err = s.ReserveExecAsk(bg, "default", session, owner, owner, "e1", 0, 1)
					}
					switch {
					case err == nil:
						wins.Add(1)
					case errors.Is(err, ErrAccountingMode), errors.Is(err, ErrLimit):
					default:
						t.Errorf("%s round %d: %v", path, round, err)
					}
				}()
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("%s round %d: %d winners for one dollar", path, round, wins.Load())
			}
		}
	}
}
