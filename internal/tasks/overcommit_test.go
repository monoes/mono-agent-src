package tasks

import (
	"errors"
	"math"
	"testing"
)

func TestOverCommittedNamesTheAncestorAndTheDollars(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	a := rkid(t, s, db, root, 10, "running")
	rkid(t, s, db, root, 10, "running")
	oc, err := s.OverCommitOf(bg, "default", a)
	if err != nil || !oc.Over || oc.Ancestor != root || oc.Session != a {
		t.Fatalf("child under an over-spoken root: %+v, %v", oc, err)
	}
	if math.Abs(oc.BudgetUSD-10) > 1e-9 || math.Abs(oc.CommittedUSD-20) > 1e-9 || math.Abs(oc.OverByUSD-10) > 1e-9 {
		t.Fatalf("dollars: %+v", oc)
	}
	// the root itself is over-committed too, by itself
	own, err := s.OverCommitOf(bg, "default", root)
	if err != nil || !own.Over || own.Ancestor != root || math.Abs(own.OverByUSD-10) > 1e-9 {
		t.Fatalf("the root asked about itself: %+v, %v", own, err)
	}
}

func TestOverCommittedIsFalseWhenThereIsRoom(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	a := rkid(t, s, db, root, 4, "running")
	for _, id := range []int64{root, a} {
		if oc, err := s.OverCommitOf(bg, "default", id); err != nil || oc.Over {
			t.Fatalf("session %d: %+v, %v", id, oc, err)
		}
	}
	free := rsession(t, s, db, 0)
	if oc, err := s.OverCommitOf(bg, "default", free); err != nil || oc.Over {
		t.Fatalf("no budget: %+v, %v", oc, err)
	}
	// exactly at the budget is committed, not over
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 6 WHERE id = ?`, root); err != nil {
		t.Fatal(err)
	}
	if oc, _ := s.OverCommitOf(bg, "default", root); oc.Over {
		t.Fatalf("6 spent + 4 child = 10 of 10 is not over: %+v", oc)
	}
}

// The lead's own overshoot (the documented cap-mode limit) is what makes every carved child refused.
func TestOverCommittedSeesTheLeadsOwnOvershoot(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	a := rkid(t, s, db, root, 6.4, "running")
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 5 WHERE id = ?`, root); err != nil {
		t.Fatal(err)
	}
	oc, err := s.OverCommitOf(bg, "default", a)
	if err != nil || !oc.Over || oc.Ancestor != root || math.Abs(oc.OverByUSD-1.4) > 1e-6 {
		t.Fatalf("%+v, %v", oc, err)
	}
}

// The helper answers from the same walk as the refusal: ExecCap's error is the typed one and names the same ancestor.
func TestExecCapRefusalIsTheTypedOverCommit(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	a := rkid(t, s, db, root, 10, "running")
	rkid(t, s, db, root, 10, "running")
	_, _, err := s.ExecCap(bg, "default", a)
	var oe *OverCommitError
	if !errors.Is(err, ErrLimit) || !errors.As(err, &oe) || oe.Ancestor != root || oe.Session != a {
		t.Fatalf("want a typed ErrLimit naming the root, got %v", err)
	}
}
