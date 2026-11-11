package dynorg

import (
	"errors"
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A cap-mode run is bounded by every budgeted ancestor as well as by its own session (#1160 R4): the number
// is what a reservation on the session could be granted, so a cap run never gets more than a reservation.

// capKid makes a child session of parent with the budget and spend, directly in the rows.
func (r *missionRig) capKid(parent int64, budget, spent float64) int64 {
	r.t.Helper()
	se, err := r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: "kid", Goal: "part", ParentSessionID: parent}, operator)
	if err != nil {
		r.t.Fatal(err)
	}
	id := se.ID
	if _, err := r.db.Exec(`UPDATE task_sessions SET budget_usd = ?, spent_usd = ?, state = 'running' WHERE id = ?`, budget, spent, id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *missionRig) setSpent(id int64, spent float64) {
	r.t.Helper()
	if _, err := r.db.Exec(`UPDATE task_sessions SET spent_usd = ? WHERE id = ?`, spent, id); err != nil {
		r.t.Fatal(err)
	}
}

func (r *missionRig) capOf(id int64) (float64, bool, error) {
	return r.c.missionExecCap(tbg, &worker{id: "w", task: &missionBind{session: id}})
}

func (r *missionRig) wantCap(id int64, want float64) {
	r.t.Helper()
	left, bounded, err := r.capOf(id)
	if err != nil || !bounded || math.Abs(left-want) > 1e-9 {
		r.t.Fatalf("cap of session #%d = %v, %v, %v; want $%v", id, left, bounded, err, want)
	}
}

func (r *missionRig) wantRefused(id int64) {
	r.t.Helper()
	left, _, err := r.capOf(id)
	if !errors.Is(err, errBudgetRefused) {
		r.t.Fatalf("cap of session #%d = %v, %v; want a budget refusal", id, left, err)
	}
}

// P has $10 and two children have $10 each: the children's budgets add up to $20, so P is over-spoken-for and
// no cap run on either child may start. Before the fix each read its own $10 and the subtree spent $20.
func TestMissionExecCapOfTwoChildrenThatEachHoldTheParentsWholeBudgetIsRefused(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	c1, c2 := r.capKid(r.sid, 10, 0), r.capKid(r.sid, 10, 0)
	r.wantRefused(c1)
	r.wantRefused(c2)
}

// Children whose budgets fit in the parent keep their own budget as the cap, and the parent's spend counts.
func TestMissionExecCapOfChildrenThatFitInTheParentIsTheirOwnBudget(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	c1, c2 := r.capKid(r.sid, 4, 0), r.capKid(r.sid, 4, 0)
	r.wantCap(c1, 4)
	r.wantCap(c2, 4)
	r.setSpent(r.sid, 2) // the parent's own workers spent $2: 2 + 4 + 4 = 10
	r.wantCap(c1, 4)
	r.setSpent(r.sid, 3) // 3 + 4 + 4 = 11: over
	r.wantRefused(c1)
}

// A child under a budgeted parent but with no budget of its own is bounded by what the parent has left, not
// unbounded: parent $10, $7 spent, so $3.
func TestMissionExecCapOfAChildWithoutBudgetIsWhatTheParentHasLeft(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.setSpent(r.sid, 7)
	k := r.capKid(r.sid, 0, 0)
	r.wantCap(k, 3)
}

// The parent has $9 spent of $10 and the child $8: the child may not spend $8 on top of the $9.
func TestMissionExecCapOfAChildLargerThanTheParentsRemainderIsRefused(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.setSpent(r.sid, 9)
	r.wantRefused(r.capKid(r.sid, 8, 0))
}

// Down a chain: the grandchild is held by its parent, which is held by the root.
func TestMissionExecCapWalksTheWholeChain(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	c := r.capKid(r.sid, 5, 0)
	g := r.capKid(c, 5, 0)
	r.wantCap(g, 5)
	r.setSpent(c, 3) // C is over its $5 (3 spent + the grandchild's 5): the grandchild's ancestor refuses
	r.wantRefused(g)
	r.setSpent(c, 0)
	// the grandchild of an unbudgeted middle session is bounded by the root above it
	m := r.capKid(r.sid, 0, 0)
	k := r.capKid(m, 4, 0)
	r.wantCap(k, 4)
	r.setSpent(r.sid, 2) // 2 + 5 (c) + 4 (via m) = 11: over
	r.wantRefused(k)
}

// A tree with no budget at all stays unbounded; a budgeted child under an unbudgeted root keeps its own budget.
func TestMissionExecCapOfARootWithoutBudgetIsUnchanged(t *testing.T) {
	r := newRig(t, rigOpts{})
	if _, bounded, err := r.capOf(r.sid); err != nil || bounded {
		t.Fatalf("root without budget: %v, %v", bounded, err)
	}
	k := r.capKid(r.sid, 0, 0)
	if left, bounded, err := r.capOf(k); err != nil || bounded || left != 0 {
		t.Fatalf("child of a root without budget: %v, %v, %v", left, bounded, err)
	}
	r.wantCap(r.capKid(r.sid, 5, 0), 5)
}

// A sibling that already spent its budget keeps counting against the parent.
func TestMissionExecCapCountsWhatASiblingAlreadySpent(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.capKid(r.sid, 5, 5)
	r.wantCap(r.capKid(r.sid, 3, 0), 3)
	r.wantRefused(r.capKid(r.sid, 7, 0)) // 5 + 3 + 7 = 15
}

// A session whose parent chain does not resolve (a cycle) fails closed: an error, not a cap.
func TestMissionExecCapFailsClosedOnACycleOfParents(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.capKid(r.sid, 5, 0)
	b := r.capKid(a, 5, 0)
	if _, err := r.db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b, a); err != nil {
		t.Fatal(err)
	}
	if left, bounded, err := r.capOf(b); err == nil {
		t.Fatalf("a cycle of parents gave a cap: %v, %v", left, bounded)
	}
}

// The accepted limit of cap mode (#1160 R2): a cap run holds nothing, so concurrent runs each read the same
// remainder. What bounds it is the number of concurrent runs times that remainder, and every run's remainder is
// bounded by the ancestors (one run can never exceed what the tree has left).
func TestMissionExecCapOfConcurrentRunsIsTheSameRemainderForEach(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	k := r.capKid(r.sid, 6, 0)
	r.wantCap(k, 6)
	r.wantCap(k, 6) // a second run starting beside the first reads the same $6
}
