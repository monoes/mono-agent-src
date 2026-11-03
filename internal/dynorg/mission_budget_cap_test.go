package dynorg

import (
	"math"
	"testing"
)

// The cap of one exec is the session's Commitment left, the number a worker reservation is granted from: what
// another worker has reserved is not spendable by this exec, so the two reads cannot disagree.
func TestMissionExecCapIsTheCommitmentLeft(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	w := &worker{id: "w1", task: &missionBind{session: r.sid}}
	left, bounded, err := r.c.missionExecCap(tbg, w)
	if err != nil || !bounded || math.Abs(left-10) > 1e-9 {
		t.Fatalf("cap with nothing held: %v, %v, %v", left, bounded, err)
	}
	res, err := r.store.Reserve(tbg, "default", r.sid, "owner-other", "w2", 6)
	if err != nil {
		t.Fatal(err)
	}
	com, err := r.store.Commitment(tbg, "default", r.sid)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := com.Left()
	left, bounded, err = r.c.missionExecCap(tbg, w)
	if err != nil || !bounded || math.Abs(left-want) > 1e-9 || math.Abs(left-4) > 1e-9 {
		t.Fatalf("cap with $6 held by another worker: %v, %v, %v; want the commitment's $4", left, bounded, err)
	}
	if err := r.store.Settle(tbg, "default", res.ID, "owner-other", 6); err != nil {
		t.Fatal(err)
	}
	left, _, err = r.c.missionExecCap(tbg, w)
	if err != nil || math.Abs(left-4) > 1e-9 {
		t.Fatalf("cap after the hold settled into spend: %v, %v; want $4", left, err)
	}
}

// A session with no budget is not bounded, whatever is held.
func TestMissionExecCapOfASessionWithoutBudgetIsUnbounded(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	w := &worker{id: "w1", task: &missionBind{session: r.sid}}
	if left, bounded, err := r.c.missionExecCap(tbg, w); err != nil || bounded || left != 0 {
		t.Fatalf("cap = %v, %v, %v; want unbounded", left, bounded, err)
	}
}
