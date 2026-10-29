package tasks

import "testing"

// chainOf makes a chain of sessions, the first a root, each below the one before it, with the given access
// and budget; the operator creates them.
func chainOf(t *testing.T, s *Store, levels []string, budgets []float64) []Session {
	t.Helper()
	var out []Session
	var parent int64
	for i := range levels {
		se, err := s.CreateSession(bg, "default", SessionInput{Title: "s", ParentSessionID: parent, AccessLevel: levels[i], BudgetUSD: budgets[i]}, human)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, se)
		parent = se.ID
	}
	return out
}

func lowerRoot(t *testing.T, s *Store, id int64, u SessionUpdate) {
	t.Helper()
	if _, err := s.UpdateSession(bg, "default", id, u, human); err != nil {
		t.Fatal(err)
	}
}

func clampedEvents(t *testing.T, s *Store, id int64) int {
	t.Helper()
	evs, err := s.SessionEvents(bg, "default", id, 0, human)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "clamped" {
			n++
		}
	}
	return n
}

func wantLimits(t *testing.T, s *Store, id int64, level string, budget float64) {
	t.Helper()
	got, err := s.GetSession(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessLevel != level || got.BudgetUSD != budget {
		t.Fatalf("session #%d is %s $%.2f, want %s $%.2f", id, got.AccessLevel, got.BudgetUSD, level, budget)
	}
}

// A lowered limit reaches an open session below a stopped one: the stopped session in between does not
// shield it.
func TestALoweredLimitReachesAnOpenSessionBelowAStoppedOne(t *testing.T) {
	s, _, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "coding"}, []float64{10, 8, 8})
	if _, err := s.StopSession(bg, "default", c[1].ID, human); err != nil {
		t.Fatal(err)
	}
	b, lvl := 3.0, "scoped"
	lowerRoot(t, s, c[0].ID, SessionUpdate{BudgetUSD: &b, AccessLevel: &lvl})
	wantLimits(t, s, c[2].ID, "scoped", 3)
	if n := clampedEvents(t, s, c[2].ID); n != 1 {
		t.Fatalf("%d clamped events, want 1", n)
	}
	// the closed one is not open: it is left as it was
	wantLimits(t, s, c[1].ID, "coding", 8)
	if n := clampedEvents(t, s, c[1].ID); n != 0 {
		t.Fatalf("a closed session got %d clamped events", n)
	}
}

// Two closed sessions in a four-level chain do not shield the open one at the bottom either.
func TestALoweredLimitReachesPastTwoClosedSessions(t *testing.T) {
	s, _, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "coding", "coding"}, []float64{10, 8, 8, 8})
	if _, err := s.StopSession(bg, "default", c[1].ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopSession(bg, "default", c[2].ID, human); err != nil {
		t.Fatal(err)
	}
	b := 3.0
	lowerRoot(t, s, c[0].ID, SessionUpdate{BudgetUSD: &b})
	wantLimits(t, s, c[3].ID, "coding", 3)
}

// An open session below a closed one that is already within the new limits is left alone, event included.
func TestAnOpenSessionBelowAClosedOneWithinTheNewLimitsIsLeftAlone(t *testing.T) {
	s, _, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "scoped"}, []float64{10, 8, 2})
	if _, err := s.StopSession(bg, "default", c[1].ID, human); err != nil {
		t.Fatal(err)
	}
	b, lvl := 3.0, "scoped"
	lowerRoot(t, s, c[0].ID, SessionUpdate{BudgetUSD: &b, AccessLevel: &lvl})
	wantLimits(t, s, c[2].ID, "scoped", 2)
	if n := clampedEvents(t, s, c[2].ID); n != 0 {
		t.Fatalf("an unchanged session got %d clamped events", n)
	}
}

// Only the limit that was lowered is applied: lowering the budget leaves access alone.
func TestOnlyTheLoweredLimitReachesTheSessionsBelow(t *testing.T) {
	s, _, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "coding"}, []float64{10, 8, 8})
	if _, err := s.StopSession(bg, "default", c[1].ID, human); err != nil {
		t.Fatal(err)
	}
	b := 3.0
	lowerRoot(t, s, c[0].ID, SessionUpdate{BudgetUSD: &b})
	wantLimits(t, s, c[2].ID, "coding", 3)
	lvl := "scoped"
	lowerRoot(t, s, c[0].ID, SessionUpdate{AccessLevel: &lvl})
	wantLimits(t, s, c[2].ID, "scoped", 3)
}

// A parent cycle (a store that was edited by hand) fails closed with an error; it does not loop.
func TestClampingThroughAParentCycleFailsClosed(t *testing.T) {
	s, db, _ := newTestStore(t)
	c := chainOf(t, s, []string{"coding", "coding", "coding"}, []float64{10, 8, 8})
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, c[2].ID, c[0].ID); err != nil {
		t.Fatal(err)
	}
	err := s.tx(bg, func(x dbx) error {
		return s.clampChildren(bg, x, c[0].ID, human, lowered{budget: true})
	})
	if err == nil {
		t.Fatal("a cycle was walked without an error")
	}
}
