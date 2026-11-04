package dynorg

import (
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
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

// What the session's child sessions hold is not spendable by an exec of the mission: a child task session with a
// budget of $3 takes $3 of the $10 until it is closed.
func TestMissionExecCapSubtractsWhatChildSessionsHold(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	w := &worker{id: "w1", task: &missionBind{session: r.sid}}
	if _, err := r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: "child", Goal: "part", BudgetUSD: 3, ParentSessionID: r.sid}, operator); err != nil {
		t.Fatal(err)
	}
	left, bounded, err := r.c.missionExecCap(tbg, w)
	if err != nil || !bounded || math.Abs(left-7) > 1e-9 {
		t.Fatalf("cap with a child session holding $3 of $10: %v, %v, %v; want $7", left, bounded, err)
	}
}

// The order a caller must keep, now and for the exec that reserves: the cap is read BEFORE the exec's own
// reserve. After the reserve the cap subtracts that hold, so reading after it caps the exec by its own share.
func TestMissionExecCapIsReadBeforeTheExecsOwnReserve(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	w := &worker{id: "w1", task: &missionBind{session: r.sid}}
	before, _, err := r.c.missionExecCap(tbg, w)
	if err != nil || math.Abs(before-10) > 1e-9 {
		t.Fatalf("cap before the reserve: %v, %v", before, err)
	}
	if _, err := r.store.Reserve(tbg, "default", r.sid, "owner-w1", "w1", before); err != nil {
		t.Fatal(err)
	}
	after, _, err := r.c.missionExecCap(tbg, w)
	if err != nil || after != 0 {
		t.Fatalf("cap after the exec reserved all of it: %v, %v; the cap subtracts the exec's own hold, so it must be read first", after, err)
	}
}

// The exec of a worker is capped by the whole of what the session has left: nothing is held on the session when
// the cap is read, so a change that reserves for the exec first (and then reads the cap) shrinks this number.
func TestMissionWorkerExecIsCappedBeforeAnyReserveOfItsOwn(t *testing.T) {
	r := newRig(t, rigOpts{budget: 1.0})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	c := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 1})
	r.call(c, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	eventually(t, "a's run", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	got := r.ex.budgets[len(r.ex.budgets)-1]
	r.ex.mu.Unlock()
	if math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("exec budget %v, want the session's whole $1.00: the cap is read before the exec's own reserve", got)
	}
	r.ex.release(a.ID)
}
