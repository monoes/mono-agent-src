package tasks

import (
	"errors"
	"strings"
	"testing"
)

func carveParent(t *testing.T, s *Store, budget float64) (Session, Actor) {
	t.Helper()
	p, err := s.CreateSession(bg, "default", SessionInput{Title: "parent", BudgetUSD: budget}, human)
	if err != nil {
		t.Fatal(err)
	}
	return p, Actor{Kind: Agent, Name: "lead", Session: p.ID}
}

func childSum(t *testing.T, s *Store, parent int64) (sum float64, n int) {
	t.Helper()
	all, err := s.ListSessions(bg, "default", SessionFilter{}, human)
	if err != nil {
		t.Fatal(err)
	}
	for _, se := range all {
		if se.ParentSessionID == parent {
			sum += se.BudgetUSD
			n++
		}
	}
	return
}

// Review finding 2: children are carved out of the parent's remaining budget, not each given all of it.
func TestChildrenShareTheParentsBudgetAndAreBounded(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := carveParent(t, s, 10)
	var refused error
	for i := 0; i < MaxOpenChildren+2; i++ {
		if _, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead); err != nil {
			refused = err
			break
		}
	}
	sum, n := childSum(t, s, p.ID)
	if sum > 10+1e-9 {
		t.Fatalf("%d children hold $%.2f of a $10 parent", n, sum)
	}
	if !errors.Is(refused, ErrLimit) || n > MaxOpenChildren {
		t.Fatalf("children %d, refusal %v", n, refused)
	}
}

func TestAChildGetsAShareOfWhatIsLeftAndAnExplicitBudgetIsChecked(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := carveParent(t, s, 10)
	if _, err := s.AddSessionSpend(bg, "default", p.ID, 6, "work", human); err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead)
	if err != nil || k.BudgetUSD != 2 {
		t.Fatalf("a share of the $4 left is $2: %v %+v", err, k)
	}
	_, err = s.CreateSession(bg, "default", SessionInput{Title: "big", ParentSessionID: p.ID, BudgetUSD: 3}, human)
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "$2.00") {
		t.Fatalf("$3 asked, $2 left after the sibling: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "ok", ParentSessionID: p.ID, BudgetUSD: 2}, human); err != nil {
		t.Fatalf("the rest: %v", err)
	}
}

// Lowering a parent's limits clamps its open children down, with an event.
func TestLoweringTheParentClampsItsChildren(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, err := s.CreateSession(bg, "default", SessionInput{Title: "parent", BudgetUSD: 10, AccessLevel: "coding"}, human)
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 8, AccessLevel: "coding", MaxTasks: 150}, human)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateSession(bg, "default", SessionInput{Title: "grandkid", ParentSessionID: k.ID, BudgetUSD: 5, AccessLevel: "coding", MaxTasks: 100}, human)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := s.CreateSession(bg, "default", SessionInput{Title: "sibling", ParentSessionID: p.ID, BudgetUSD: 2}, human)
	if err != nil {
		t.Fatal(err)
	}
	b, mt, lvl := 3.0, 20, "scoped"
	if _, err := s.UpdateSession(bg, "default", p.ID, SessionUpdate{BudgetUSD: &b, MaxTasks: &mt, AccessLevel: &lvl}, human); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id     int64
		budget float64
	}{{k.ID, 3}, {g.ID, 3}} {
		got, err := s.GetSession(bg, "default", c.id, human)
		if err != nil {
			t.Fatal(err)
		}
		if got.BudgetUSD != c.budget || got.MaxTasks != 20 || got.AccessLevel != "scoped" {
			t.Fatalf("session #%d not clamped: %+v", c.id, got)
		}
	}
	if sum, _ := childSum(t, s, p.ID); sum > 3.01 {
		t.Fatalf("the children still hold $%.2f of a $3 parent", sum)
	}
	if got, _ := s.GetSession(bg, "default", k2.ID, human); got.BudgetUSD > 0.01+1e-9 {
		t.Fatalf("the later sibling kept $%.2f", got.BudgetUSD)
	}
	evs, err := s.SessionEvents(bg, "default", k.ID, 0, human)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		found = found || e.Kind == "clamped"
	}
	if !found {
		t.Fatalf("no clamped event: %+v", evs)
	}
	// raising the parent never raises a child
	hi := 100.0
	if _, err := s.UpdateSession(bg, "default", p.ID, SessionUpdate{BudgetUSD: &hi}, human); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(bg, "default", k.ID, human); got.BudgetUSD != 3 {
		t.Fatalf("a child was raised: %+v", got)
	}
}
