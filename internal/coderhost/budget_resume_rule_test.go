package coderhost

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

// claude restores a resumed session's total only if that session was the last to exit in the project directory;
// otherwise the new process counts from zero. A turn's figure has the previous one subtracted only when its cost AND
// every token counter are at least the previous ones; in every other case it is charged whole (#1744).

// claudeTurn is one finished turn of a claude session as the chat journals it. tokens is the coder.usage notice's
// models object ("" = the journal holds no counters).
type claudeTurn struct{ session, cost, tokens string }

// counters is one model's session counters.
func counters(in, out, read, creation int) string {
	return fmt.Sprintf(`{"opus":{"input":%d,"output":%d,"cacheRead":%d,"cacheCreation":%d}}`, in, out, read, creation)
}

// every is counters with all four equal to n.
func every(n int) string { return counters(n, n, n, n) }

func usageNotice(models string) string {
	return `{"code":"coder.usage","message":"","severity":"info","coderUsage":{"models":` + models + `}}`
}

func journalClaudeTurn(t *testing.T, st *Store, conv, id string, at time.Time, x claudeTurn) {
	t.Helper()
	addTurn(t, st, conv, id, "completed", "x", at)
	event(t, st, conv, id, 1, "session.bound", `{"runtime":"claude","sessionId":"`+x.session+`"}`)
	event(t, st, conv, id, 2, "usage.updated", `{"agentId":"","costUsd":`+x.cost+`,"source":"result"}`)
	if x.tokens != "" {
		event(t, st, conv, id, 3, "notice", usageNotice(x.tokens))
	}
}

func spentOf(t *testing.T, turns []claudeTurn) float64 {
	t.Helper()
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	for i, x := range turns {
		journalClaudeTurn(t, st, c, fmt.Sprintf("t%d", i), base.Add(time.Duration(i)*time.Minute), x)
	}
	stampEvents(t, st)
	spent, err := st.Spent("p", c)
	if err != nil {
		t.Fatal(err)
	}
	return spent
}

func TestResumedClaudeTurnIsSubtractedOnlyWhenEveryCounterHeld(t *testing.T) {
	first := claudeTurn{"s1", "0.4", counters(100, 100, 100, 100)}
	next := func(tokens string) claudeTurn { return claudeTurn{"s1", "0.45", tokens} }
	for _, tc := range []struct {
		name  string
		turns []claudeTurn
		want  float64
	}{
		{"restored: only the increase", []claudeTurn{first, next(counters(101, 150, 900, 100))}, 0.45},
		{"nothing grew", []claudeTurn{first, {"s1", "0.4", first.tokens}}, 0.4},
		{"not restored: higher cost, fewer tokens", []claudeTurn{first, next(every(30))}, 0.85},
		{"input fell", []claudeTurn{first, next(counters(99, 200, 200, 200))}, 0.85},
		{"output fell", []claudeTurn{first, next(counters(200, 99, 200, 200))}, 0.85},
		{"cache read fell", []claudeTurn{first, next(counters(200, 200, 99, 200))}, 0.85},
		{"cache creation fell", []claudeTurn{first, next(counters(200, 200, 200, 99))}, 0.85},
		{"cost fell, tokens grew", []claudeTurn{first, {"s1", "0.1", every(900)}}, 0.5},
		{"no counters now", []claudeTurn{first, next("")}, 0.85},
		{"no counters before", []claudeTurn{{"s1", "0.4", ""}, next(every(900))}, 0.85},
		{"empty counters", []claudeTurn{{"s1", "0.4", `{}`}, next(every(900))}, 0.85},
		{"all zero counters say nothing", []claudeTurn{{"s1", "0.4", every(0)}, next(every(900))}, 0.85},
		{"a counter the runtime left out", []claudeTurn{first, next(`{"opus":{"input":900,"output":900,"cacheRead":900,"cacheCreation":900,"partial":true}}`)}, 0.85},
		// The counters are summed over the models, as the runner sums them: another model can serve a later turn.
		{"summed over models", []claudeTurn{first, next(`{"opus":{"input":100,"output":100,"cacheRead":100,"cacheCreation":100},"haiku":{"input":1,"output":1,"cacheRead":0,"cacheCreation":0}}`)}, 0.45},
		// The baseline is always the latest report: a turn without counters leaves nothing to compare the next with.
		{"after a turn without counters", []claudeTurn{first, next(""), {"s1", "0.5", every(900)}}, 0.4 + 0.45 + 0.5},
		{"after a restart from zero", []claudeTurn{first, {"s1", "0.45", every(30)}, {"s1", "0.6", every(60)}}, 0.4 + 0.45 + 0.15},
		{"journal from before counters", []claudeTurn{{"s1", "0.4", ""}, {"s1", "0.5", ""}}, 0.9},
		{"another session", []claudeTurn{first, {"s2", "0.45", every(900)}}, 0.85},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := spentOf(t, tc.turns); math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("spent = %v, want %v", got, tc.want)
			}
		})
	}
}

// Three turns that really cost 0.40, 0.45 and 0.50 under a $1 budget, each after the process started again from zero:
// the second leaves 0.15 and the third uses the budget up. Subtracting by cost alone counted 0.50 for all three and
// kept admitting turns.
func TestCapBoundsTheRealSpendOfResumesThatWereNotRestored(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	if err := st.SetBudget("p", c, 1); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i, x := range []struct {
		turn      claudeTurn
		spent     float64
		remaining float64
	}{
		{claudeTurn{"s1", "0.40", every(900)}, 0.40, 0.60},
		{claudeTurn{"s1", "0.45", every(300)}, 0.85, 0.15},
		{claudeTurn{"s1", "0.50", every(200)}, 1.35, 0},
	} {
		journalClaudeTurn(t, st, c, fmt.Sprintf("t%d", i), base.Add(time.Duration(i)*time.Minute), x.turn)
		stampEvents(t, st)
		if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-x.spent) > 1e-9 {
			t.Fatalf("after turn %d: spent = %v %v, want %v", i+1, spent, err, x.spent)
		}
		left, capped, err := st.TurnCap("p", c)
		if x.remaining == 0 {
			if !errors.Is(err, errBudgetUsedUp) {
				t.Fatalf("after turn %d the next turn got $%v (%v), want the budget used up", i+1, left, err)
			}
			continue
		}
		if err != nil || !capped || math.Abs(left-x.remaining) > 1e-9 {
			t.Fatalf("after turn %d the next turn's cap = %v %v %v, want %v", i+1, left, capped, err, x.remaining)
		}
	}
}

// A compaction is a second run on the turn's session: it is judged against the turn's own report, and the next turn
// against the compaction's.
func TestCompactionIsJudgedByTheSameRule(t *testing.T) {
	followUp := func(total, session, cost, tokens string) string {
		return `{"agentId":"","costUsd":` + total + `,"source":"result","followUp":{"sessionId":"` + session + `","costUsd":` + cost + `,"models":` + tokens + `}}`
	}
	for _, tc := range []struct {
		name   string
		follow string
		next   claudeTurn
		want   float64
	}{
		{"restored", followUp("0.5", "s1", "0.5", every(500)), claudeTurn{"s1", "0.6", every(600)}, 0.6},
		// 0.4 + 0.5, then 0.1 over the compaction's total
		{"not restored", followUp("0.9", "s1", "0.5", every(100)), claudeTurn{"s1", "0.6", every(150)}, 1.0},
		// the next turn is above the turn's own report but below the compaction's: whole
		{"next judged against the compaction", followUp("0.5", "s1", "0.5", every(500)), claudeTurn{"s1", "0.6", every(450)}, 1.1},
		{"no counters", followUp("0.9", "s1", "0.5", `{}`), claudeTurn{"s1", "0.6", every(900)}, 1.5},
		// another session: added whole, and the turn's session still ends where its own run did
		{"another session", followUp("0.9", "s2", "0.5", every(500)), claudeTurn{"s1", "0.6", every(600)}, 1.1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStoreT(t)
			m, _ := master(t, st, "coding")
			c := m.ConversationID
			base := time.Now().Add(-time.Hour)
			journalClaudeTurn(t, st, c, "t0", base, claudeTurn{"s1", "0.4", every(400)})
			event(t, st, c, "t0", 4, "usage.updated", tc.follow)
			journalClaudeTurn(t, st, c, "t1", base.Add(time.Minute), tc.next)
			stampEvents(t, st)
			if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-tc.want) > 1e-9 {
				t.Errorf("spent = %v %v, want %v", spent, err, tc.want)
			}
		})
	}
}

// A turn compacted before the compaction's report was journaled holds two different "result" figures and only the
// first run's counters, so what its session ended at is unknown: the turn after it is charged whole.
func TestAnOldCompactedTurnLeavesNothingToSubtract(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	journalClaudeTurn(t, st, c, "t0", base, claudeTurn{"s1", "0.4", every(400)})
	event(t, st, c, "t0", 4, "usage.updated", `{"agentId":"","costUsd":0.5,"source":"result"}`)
	journalClaudeTurn(t, st, c, "t1", base.Add(time.Minute), claudeTurn{"s1", "0.6", every(600)})
	stampEvents(t, st)
	if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-1.1) > 1e-9 {
		t.Errorf("spent = %v %v, want 0.5 + 0.6", spent, err)
	}
}

// A turn that journaled two different sets of counters gives no way to say which are the session's: charged whole.
func TestATurnWithTwoSetsOfCountersIsChargedInFull(t *testing.T) {
	for name, sets := range map[string][2]string{"high first": {every(900), every(30)}, "high last": {every(30), every(900)}} {
		t.Run(name, func(t *testing.T) {
			st := newStoreT(t)
			m, _ := master(t, st, "coding")
			c := m.ConversationID
			base := time.Now().Add(-time.Hour)
			journalClaudeTurn(t, st, c, "t0", base, claudeTurn{"s1", "0.4", every(400)})
			journalClaudeTurn(t, st, c, "t1", base.Add(time.Minute), claudeTurn{"s1", "0.45", sets[0]})
			event(t, st, c, "t1", 4, "notice", usageNotice(sets[1]))
			stampEvents(t, st)
			if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-0.85) > 1e-9 {
				t.Errorf("spent = %v %v, want 0.4 + 0.45", spent, err)
			}
		})
	}
}

// The previous turn of a session is the one to compare with: when it recorded no cost (stopped, crashed), nothing is
// known about where the session ended, so the next turn is charged whole instead of being compared with an older one.
func TestATurnAfterOneThatRecordedNoCostIsChargedInFull(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	journalClaudeTurn(t, st, c, "t0", base, claudeTurn{"s1", "0.4", every(400)})
	addTurn(t, st, c, "t1", "cancelled", "x", base.Add(time.Minute))
	event(t, st, c, "t1", 1, "session.bound", `{"runtime":"claude","sessionId":"s1"}`)
	journalClaudeTurn(t, st, c, "t2", base.Add(2*time.Minute), claudeTurn{"s1", "0.5", every(900)})
	journalClaudeTurn(t, st, c, "t3", base.Add(3*time.Minute), claudeTurn{"s1", "0.6", every(950)})
	stampEvents(t, st)
	if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-1.0) > 1e-9 {
		t.Errorf("spent = %v %v, want 0.4 + 0.5 + 0.1", spent, err)
	}
}

// A turn whose cost is marked as its own (monomind's agent-exec-turn-cost) is charged as reported, and the turn after
// it has no session total to be compared with.
func TestATurnsOwnCostIsNeverReduced(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	journalClaudeTurn(t, st, c, "t0", base, claudeTurn{"s1", "0.4", every(400)})
	for i, id := range []string{"t1", "t2"} {
		cost := []string{"0.45", "0.5"}[i]
		addTurn(t, st, c, id, "completed", "x", base.Add(time.Duration(i+1)*time.Minute))
		event(t, st, c, id, 1, "session.bound", `{"runtime":"claude","sessionId":"s1"}`)
		event(t, st, c, id, 2, "usage.updated", `{"agentId":"","costUsd":`+cost+`,"source":"result","costPerTurn":true}`)
		event(t, st, c, id, 3, "notice", usageNotice(every(900)))
	}
	journalClaudeTurn(t, st, c, "t3", base.Add(3*time.Minute), claudeTurn{"s1", "0.6", every(1000)})
	stampEvents(t, st)
	if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-1.95) > 1e-9 {
		t.Errorf("spent = %v %v, want every turn whole, 1.95", spent, err)
	}
}
