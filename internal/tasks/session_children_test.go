package tasks

import (
	"errors"
	"strings"
	"testing"
)

func treeParent(t *testing.T, s *Store, budget float64) (Session, Actor) {
	t.Helper()
	p, err := s.CreateSession(bg, "default", SessionInput{Title: "parent", BudgetUSD: budget}, human)
	if err != nil {
		t.Fatal(err)
	}
	return p, Actor{Kind: Agent, Name: "lead", Session: p.ID}
}

func kidOf(t *testing.T, s *Store, parent int64, budget float64) Session {
	t.Helper()
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: parent, BudgetUSD: budget}, human)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Lowering a parent's limits clamps its open children down, with an event; a budget is clamped child by
// child to the parent's, and the children's budgets are not summed.
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
	if got, _ := s.GetSession(bg, "default", k2.ID, human); got.BudgetUSD != 2 {
		t.Fatalf("a budget already within the parent's was changed: $%.2f", got.BudgetUSD)
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

// A budget is the operator's at every level: an agent cannot name one for a session, top-level or below
// its own, and what it creates has none (0, unset). The existing budget verb is the operator's too.
func TestAnAgentCannotNameOrChangeABudget(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := treeParent(t, s, 10)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID, BudgetUSD: 1}, lead); !errors.Is(err, ErrOperatorOnly) || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("an agent named a child's budget: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "top", BudgetUSD: 1}, Actor{Kind: Agent, Name: "loose"}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent named a top-level budget: %v", err)
	}
	k, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead)
	if err != nil || k.BudgetUSD != 0 {
		t.Fatalf("an agent's child has budget %v: %v", k.BudgetUSD, err)
	}
	for _, who := range []Actor{lead, {Kind: Agent, Name: "kid-lead", Session: k.ID}} {
		for _, id := range []int64{p.ID, k.ID} {
			b := 5.0
			if _, err := s.UpdateSession(bg, "default", id, SessionUpdate{BudgetUSD: &b}, who); !errors.Is(err, ErrOperatorOnly) {
				t.Fatalf("%s changed the budget of #%d: %v", who.Name, id, err)
			}
		}
	}
}

// Under a parent with a budget the operator sets a child's budget above 0 and at most the parent's; the
// children are each held to the parent's, and their sum is not checked.
func TestAChildsBudgetStaysWithinItsBudgetedParent(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, _ := treeParent(t, s, 10)
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "big", ParentSessionID: p.ID, BudgetUSD: 10.5}, human); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "$10.50") || !strings.Contains(err.Error(), "$10.00") {
		t.Fatalf("a child's budget above the parent's, at creation: %v", err)
	}
	k := kidOf(t, s, p.ID, 4)
	f := func(v float64) *float64 { return &v }
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: f(10.5)}, human); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "$10.50") || !strings.Contains(err.Error(), "$10.00") {
		t.Fatalf("a child's budget above the parent's, at update: %v", err)
	}
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: f(0)}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("no cap under a capped parent: %v", err)
	}
	if got, _ := s.GetSession(bg, "default", k.ID, human); got.BudgetUSD != 4 {
		t.Fatalf("a refused update changed the budget: %v", got.BudgetUSD)
	}
	// the sum is not checked: a second child may take the parent's whole budget
	if got, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: f(10)}, human); err != nil || got.BudgetUSD != 10 {
		t.Fatalf("the parent's whole budget: %v %v", got.BudgetUSD, err)
	}
	kidOf(t, s, p.ID, 10)
	// an unbudgeted parent still lets the operator set any child budget, 0 included
	free, _ := treeParent(t, s, 0)
	c := kidOf(t, s, free.ID, 50)
	if got, err := s.UpdateSession(bg, "default", c.ID, SessionUpdate{BudgetUSD: f(0)}, human); err != nil || got.BudgetUSD != 0 {
		t.Fatalf("an unbudgeted parent: %v %v", got.BudgetUSD, err)
	}
}

// Approving a draft child that has no budget of its own under a parent that has one is refused, with
// the verb that sets it: no unlimited child under a capped parent, without any accounting.
func TestApprovingAnUnbudgetedChildUnderABudgetedParentIsRefused(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, _ := treeParent(t, s, 10)
	k := kidOf(t, s, p.ID, 0)
	mustTask(t, s, "default", k.ID, "t")
	_, err := s.ApproveSession(bg, "default", k.ID, human)
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "task session budget") || !strings.Contains(err.Error(), "$10.00") {
		t.Fatalf("approved an unbudgeted child of a budgeted parent: %v", err)
	}
	if got, _ := s.GetSession(bg, "default", k.ID, human); got.State != SessionDraft {
		t.Fatalf("a refused approval moved the child to %s", got.State)
	}
	b := 3.0
	if _, err := s.UpdateSession(bg, "default", k.ID, SessionUpdate{BudgetUSD: &b}, human); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ApproveSession(bg, "default", k.ID, human); err != nil || got.State != SessionRunning {
		t.Fatalf("approval after setting a budget: %v %v", got.State, err)
	}
	// an unbudgeted parent does not ask for it
	free, _ := treeParent(t, s, 0)
	c := kidOf(t, s, free.ID, 0)
	mustTask(t, s, "default", c.ID, "t")
	if _, err := s.ApproveSession(bg, "default", c.ID, human); err != nil {
		t.Fatalf("a child of an unbudgeted parent: %v", err)
	}
}

// The fence on open children holds with no budget to run out: a parent without a budget hands out any
// amount, so only MaxOpenChildren stops an agent that keeps starting children. The operator is not held to it.
func TestAnAgentIsStoppedAtTheOpenChildFenceEvenWithNoBudget(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, lead := treeParent(t, s, 0)
	for i := 0; i < MaxOpenChildren; i++ {
		if _, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, lead); err != nil {
			t.Fatalf("child %d: %v", i+1, err)
		}
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "one more", ParentSessionID: p.ID}, lead); !errors.Is(err, ErrLimit) {
		t.Fatalf("an agent started child %d: %v", MaxOpenChildren+1, err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "operator's", ParentSessionID: p.ID}, human); err != nil {
		t.Fatalf("the operator is held to the agents' fence: %v", err)
	}
}
