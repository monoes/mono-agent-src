package dynorg

import (
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A parent whose child holds $9 of its $10 is issued a worker cap of the $1 left, the run holds that
// $1 while it works (a carve cannot take it), and settling it leaves the cost as spend and no reservation.
func TestMissionWorkerCapIsWhatIsLeftAfterChildrenAndIsHeldWhileItRuns(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if _, err := r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: "kid", ParentSessionID: r.sid, BudgetUSD: 9}, operator); err != nil {
		t.Fatal(err)
	}
	c := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 1})
	r.call(c, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	eventually(t, "a's run", func() bool { return r.ex.runCount(a.ID) == 1 })
	r.ex.mu.Lock()
	got := r.ex.budgets[len(r.ex.budgets)-1]
	r.ex.mu.Unlock()
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("the worker's cap is $%v, want the $1 left after the child's $9", got)
	}
	if se := r.session(); math.Abs(se.ReservedUSD-1) > 1e-9 {
		t.Fatalf("reserved %v while the worker runs, want 1", se.ReservedUSD)
	}
	if _, err := r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: "late", ParentSessionID: r.sid, BudgetUSD: 0.5}, operator); err == nil {
		t.Fatal("a child was carved out of the running worker's reservation")
	}
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the reservation to settle", func() bool { return r.session().ReservedUSD == 0 })
	if se := r.session(); se.SpentUSD <= 0 || se.SpentUSD > 1 {
		t.Fatalf("spent %v, want the run's cost", se.SpentUSD)
	}
}

// A worker that crashes leaves no reservation, and one a dead process left is released by the next
// reconcile of the session, when no worker of this Conductor runs in it.
func TestACrashedWorkersReservationIsReleased(t *testing.T) {
	r := newRig(t, rigOpts{budget: 5})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.fail[a.ID] = 1
	c := r.conductor(Limits{MaxAgents: 5, MaxConcurrent: 1})
	r.call(c, ToolMissionAssign, `{"task_id":`+itoa(a.ID)+`,"executor":"worker"}`)
	eventually(t, "the crashed run", func() bool { return r.ex.runCount(a.ID) >= 1 })
	eventually(t, "the crashed worker's reservation to be settled", func() bool { return r.session().ReservedUSD == 0 && c.taskWorker(a.ID) == nil })

	// a process that died with a worker running leaves its reservation in the store
	if _, err := r.store.ReserveSpend(tbg, "default", r.sid, 2); err != nil {
		t.Fatal(err)
	}
	if got := r.session().ReservedUSD; got != 2 {
		t.Fatalf("reserved %v, want 2", got)
	}
	c.missionReconcile(tbg, r.sid)
	if got := r.session().ReservedUSD; got != 0 {
		t.Fatalf("the dead process's reservation stayed: %v", got)
	}
}
