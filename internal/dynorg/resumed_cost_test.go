package dynorg

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// claude reports a resumed session's cost as a running total for the whole session (#1744), so a worker run that
// resumes its session costs what it added to that total.

// totalRun is one run of totalsExec: the session it reports ("" = none), that session's total so far, and its token
// counters (0 = they grew with the total, as a restored session's do; -1 = none reported). killed ends the run after
// the usage event, with no result.
type totalRun struct {
	session string
	total   float64
	noCost  bool
	tokens  int64
	killed  bool
}

func (r totalRun) models() map[string]monomind.ModelUsage {
	n := r.tokens
	if n < 0 || r.noCost {
		return nil
	}
	if n == 0 {
		n = int64(r.total * 1000)
	}
	return map[string]monomind.ModelUsage{"opus": {Input: n, Output: n, CacheRead: n, CacheCreation: n}}
}

// totalsExec is a claude-like exec: each run reports its session, then the session's total as usage and result.
type totalsExec struct {
	mu    sync.Mutex
	runs  []totalRun
	calls []monomind.ExecOptions
}

func (e *totalsExec) exec(_ context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	e.mu.Lock()
	run := e.runs[min(len(e.calls), len(e.runs)-1)]
	e.calls = append(e.calls, o)
	e.mu.Unlock()
	on(monomind.Event{Type: monomind.EventStart})
	if run.session != "" {
		on(monomind.Event{Type: monomind.EventSession, SessionID: run.session})
	}
	if !run.noCost {
		on(usageCost(run.total))
	}
	if run.killed {
		return &monomind.TurnResult{SessionID: run.session, CostUSD: run.total, HasCostUSD: !run.noCost}, nil
	}
	if !run.noCost {
		on(monomind.Event{Type: monomind.EventResult, HasCostUSD: true, CostUSD: run.total, StopReason: monomind.StopEndTurn,
			ResultFields: monomind.ResultFields{ModelUsage: run.models()}})
	}
	return &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, SessionID: run.session,
		CostUSD: run.total, HasCostUSD: !run.noCost, ModelUsage: run.models()}, nil
}

func (e *totalsExec) caps() []float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []float64
	for _, o := range e.calls {
		out = append(out, o.BudgetUSD)
	}
	return out
}

func finishedCosts(em *recEmitter) []float64 {
	var out []float64
	for _, p := range em.find(chatevents.EventAgentFinished) {
		if c := p.(chatevents.AgentFinishedPayload).CostUSD; c != nil {
			out = append(out, *c)
		}
	}
	return out
}

func largestWorkerUsage(em *recEmitter) float64 {
	largest := 0.0
	for _, p := range em.find(chatevents.EventUsageUpdated) {
		if c := p.(chatevents.UsageUpdatedPayload).CostUSD; c != nil {
			largest = max(largest, *c)
		}
	}
	return largest
}

func nearAll(got []float64, want ...float64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !spendNear(got[i], want[i]) {
			return false
		}
	}
	return true
}

func follow(t *testing.T, c *Conductor) {
	t.Helper()
	if _, err := c.Message(context.Background(), "w1", "more"); err != nil {
		t.Fatal(err)
	}
	c.Wait(context.Background(), []string{"w1"}, 5*time.Second)
}

func spawnOne(t *testing.T, c *Conductor, m Model) {
	t.Helper()
	if _, err := c.Spawn(context.Background(), SpawnRequest{Brief: "implement", Runtime: m.Runtime, Model: m.Model, Wait: true}); err != nil {
		t.Fatal(err)
	}
}

// Three runs that really cost 0.40 each: the $1 budget lets the third start with the 0.20 left and refuses the
// fourth. Adding up the reported totals (0.40 + 0.80) would refuse the third.
func TestResumedClaudeFollowUpsAreCountedOnceAndTheBudgetStillBounds(t *testing.T) {
	ex := &totalsExec{runs: []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.8}, {session: "s1", total: 1.2}}}
	em := &recEmitter{}
	c := New(context.Background(), Config{Cwd: "/w", Limits: Limits{MaxAgents: 1, MaxConcurrent: 1, BudgetUSD: 1},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Exec: ex.exec, Emit: em})
	t.Cleanup(c.Close)
	spawnOne(t, c, opus)
	follow(t, c)
	if got := c.spent(); !spendNear(got, 0.8) {
		t.Fatalf("spent after two runs = %v, want 0.8", got)
	}
	follow(t, c)
	if _, err := c.Message(context.Background(), "w1", "again"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("a follow-up past the real spend: %v", err)
	}
	if got := ex.caps(); !nearAll(got, 1, 0.6, 0.2) {
		t.Errorf("caps given to the runs = %v, want 1, 0.6, 0.2 (what is really left)", got)
	}
	if got := c.spent(); !spendNear(got, 1.2) {
		t.Errorf("spent = %v, want the session's total 1.2", got)
	}
	if got := finishedCosts(em); !nearAll(got, 0.4, 0.8, 1.2) {
		t.Errorf("agent.finished costs = %v", got)
	}
	if got := largestWorkerUsage(em); !spendNear(got, 1.2) {
		t.Errorf("largest worker usage.updated = %v, want 1.2", got)
	}
}

// A veteran resumes its session in a later turn: that turn is charged only what its runs added.
func TestVeteranClaudeFollowUpIsChargedOnlyWhatItAdded(t *testing.T) {
	kept := &keptWorkers{}
	ex := &totalsExec{runs: []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.7}, {session: "s1", total: 0.9}}}
	turn := func() (*Conductor, *recEmitter) {
		em := &recEmitter{}
		c := New(context.Background(), Config{Cwd: "/w", Limits: Limits{MaxAgents: 3, MaxConcurrent: 1, BudgetUSD: 1},
			Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Exec: ex.exec, Emit: em, Remember: kept.remember})
		t.Cleanup(c.Close)
		return c, em
	}
	first, _ := turn()
	spawnOne(t, first, opus)
	first.Close()

	second, em := turn()
	second.AddVeterans(kept.list())
	follow(t, second)
	if got := second.spent(); !spendNear(got, 0.3) {
		t.Fatalf("the second turn spent %v, want 0.3", got)
	}
	second.Close()

	third, em3 := turn()
	third.AddVeterans(kept.list())
	follow(t, third)
	if got := third.spent(); !spendNear(got, 0.2) {
		t.Fatalf("the third turn spent %v, want 0.2", got)
	}
	if got := append(finishedCosts(em), finishedCosts(em3)...); !nearAll(got, 0.3, 0.2) {
		t.Errorf("agent.finished costs = %v, want 0.3 then 0.2", got)
	}
}

// Only the same claude session evidently continuing is subtracted; every doubt is charged in full.
func TestResumedCostDoubtfulCasesAreChargedInFull(t *testing.T) {
	codex := Model{Runtime: "codex", Model: "gpt", FullAccess: true, Resume: true, ReportsCost: true}
	for _, tc := range []struct {
		name  string
		model Model
		runs  []totalRun
		want  float64
	}{
		{"another session", opus, []totalRun{{session: "s1", total: 0.4}, {session: "s2", total: 0.6}}, 1.0},
		{"no session reported", opus, []totalRun{{session: "s1", total: 0.4}, {total: 0.6}}, 1.0},
		{"a figure that fell", opus, []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.1}, {session: "s1", total: 0.3}}, 0.7},
		{"a run with no cost", opus, []totalRun{{session: "s1", total: 0.4}, {session: "s1", noCost: true}, {session: "s1", total: 0.9}}, 0.9},
		{"not restored: higher cost, fewer tokens", opus, []totalRun{{session: "s1", total: 0.4, tokens: 900}, {session: "s1", total: 0.6, tokens: 300}}, 1.0},
		{"no counters now", opus, []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.6, tokens: -1}}, 1.0},
		{"no counters before", opus, []totalRun{{session: "s1", total: 0.4, tokens: -1}, {session: "s1", total: 0.6}}, 1.0},
		{"killed before its counters", opus, []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.6, killed: true}}, 1.0},
		{"another runtime", codex, []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.6}}, 1.0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &totalsExec{runs: tc.runs}
			c := New(context.Background(), Config{Cwd: "/w", Staffer: &Staffer{Roster: []Model{tc.model}, Lead: tc.model},
				Exec: ex.exec, Emit: &recEmitter{}})
			t.Cleanup(c.Close)
			spawnOne(t, c, tc.model)
			for range tc.runs[1:] {
				follow(t, c)
			}
			if len(ex.calls) != len(tc.runs) || ex.calls[1].Resume != "s1" {
				t.Fatalf("calls %d, second resumed %q", len(ex.calls), ex.calls[1].Resume)
			}
			if got := c.spent(); !spendNear(got, tc.want) {
				t.Errorf("spent = %v, want %v", got, tc.want)
			}
		})
	}
}

// A mission worker that resumes its session charges the mission's meter once, on the cap and on a reservation; the
// session's budget still bounds the runs by what was really spent.
func TestMissionResumedClaudeWorkerIsChargedOnce(t *testing.T) {
	for _, reserve := range []bool{false, true} {
		name := "cap"
		if reserve {
			name = "reservation"
		}
		t.Run(name, func(t *testing.T) {
			build := newRig
			if reserve {
				build = newResvRig
			}
			r := build(t, rigOpts{budget: 1})
			w := directWorker(r, "w1")
			r.approve()
			ex := &totalsExec{runs: []totalRun{{session: "s1", total: 0.4}, {session: "s1", total: 0.8}, {session: "s1", total: 1.05}}}
			c := New(tbg, Config{Cwd: "/w", Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, Exec: ex.exec, Emit: r.em,
				Missions: r.mc, Now: r.clk.now})
			t.Cleanup(c.Close)
			run := func(resume string) error {
				_, err := c.execOnce(tbg, w, opus, "doing task", resume)
				c.recordSpend(tbg, r.mc, w)
				return err
			}
			for i, want := range []float64{0.4, 0.8, 1.05} {
				resume := ""
				if i > 0 {
					resume = "s1"
				}
				if err := run(resume); err != nil {
					t.Fatalf("run %d: %v", i+1, err)
				}
				if got := r.session().SpentUSD; !spendNear(got, want) {
					t.Fatalf("after run %d the session spent %v, want %v", i+1, got, want)
				}
			}
			if got := ex.caps(); !nearAll(got, 1, 0.6, 0.2) {
				t.Errorf("caps given to the runs = %v, want 1, 0.6, 0.2", got)
			}
			if err := run("s1"); !errors.Is(err, errBudgetRefused) || len(ex.calls) != 3 {
				t.Errorf("a run on the spent mission: %v after %d runs", err, len(ex.calls))
			}
		})
	}
}

// Runs that really cost 0.40, 0.45 and 0.50, each after the session's total started again from zero: the second leaves
// 0.15, the third passes the $1 budget and a fourth is refused. Subtracting by cost alone counted 0.50 for the three.
func TestFollowUpsThatWereNotRestoredAreChargedInFullAndTheBudgetBoundsThem(t *testing.T) {
	ex := &totalsExec{runs: []totalRun{{session: "s1", total: 0.4, tokens: 900}, {session: "s1", total: 0.45, tokens: 300},
		{session: "s1", total: 0.5, tokens: 200}}}
	c := New(context.Background(), Config{Cwd: "/w", Limits: Limits{MaxAgents: 1, MaxConcurrent: 1, BudgetUSD: 1},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Exec: ex.exec, Emit: &recEmitter{}})
	t.Cleanup(c.Close)
	spawnOne(t, c, opus)
	follow(t, c)
	follow(t, c)
	if _, err := c.Message(context.Background(), "w1", "again"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("a follow-up past the real spend: %v", err)
	}
	if got := ex.caps(); !nearAll(got, 1, 0.6, 0.15) {
		t.Errorf("caps given to the runs = %v, want 1, 0.6, 0.15", got)
	}
	if got := c.spent(); !spendNear(got, 1.35) {
		t.Errorf("spent = %v, want 1.35", got)
	}
}
