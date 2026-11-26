package coderhost

import (
	"math"
	"testing"
	"time"
)

// stampEvents gives each event its turn's creation time, as the journal's own timestamps order the turns.
func stampEvents(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE ai_chat_events SET created_at = (SELECT created_at FROM ai_chat_turns WHERE id = turn_id)`); err != nil {
		t.Fatal(err)
	}
}

// claude reports a resumed session's cost as a running total for the whole session, so a conversation's turns
// are charged what each added over the one before in the same runtime session (#1744).
func TestSpendOfResumedClaudeTurnsIsTheDeltaOfTheSameSession(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	// A restored total comes with token counters that did not fall.
	for i, cost := range []string{"0.057", "0.203", "0.219", "0.268"} {
		journalClaudeTurn(t, st, c, "t"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Minute), claudeTurn{"s1", cost, every(100 * (i + 1))})
	}
	stampEvents(t, st)
	spent, err := st.Spent("p", c)
	if err != nil || math.Abs(spent-0.268) > 1e-9 {
		t.Fatalf("spent = %v %v, want 0.268 (the session's total, not the sum of its running totals)", spent, err)
	}
}

// A new runtime session starts from zero, a figure that falls is not a running total, and a turn that never
// bound a session is charged in full: the delta is taken only where the same session evidently continues.
func TestSpendDeltaIsTakenOnlyWithinOneSession(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	c := m.ConversationID
	base := time.Now().Add(-time.Hour)
	turns := []struct{ id, session, cost, tokens string }{
		{"t1", "s1", "0.5", every(500)},
		{"t2", "s2", "0.3", every(300)}, // another session: charged in full
		{"t3", "s1", "0.7", every(700)}, // s1 again: +0.2
		{"t4", "s1", "0.1", every(100)}, // fell: not a running total, charged in full
		{"t5", "", "0.4", every(900)},   // no session bound: charged in full
	}
	for i, x := range turns {
		at := base.Add(time.Duration(i) * time.Minute)
		if x.session != "" {
			journalClaudeTurn(t, st, c, x.id, at, claudeTurn{x.session, x.cost, x.tokens})
			continue
		}
		addTurn(t, st, c, x.id, "completed", "x", at)
		event(t, st, c, x.id, 2, "usage.updated", `{"agentId":"","costUsd":`+x.cost+`}`)
		event(t, st, c, x.id, 3, "notice", usageNotice(x.tokens))
	}
	stampEvents(t, st)
	want := 0.5 + 0.3 + 0.2 + 0.1 + 0.4
	if spent, err := st.Spent("p", c); err != nil || math.Abs(spent-want) > 1e-9 {
		t.Fatalf("spent = %v %v, want %v", spent, err, want)
	}
}
