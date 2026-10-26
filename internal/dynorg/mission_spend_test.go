package dynorg

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

func usageCost(usd float64) monomind.Event {
	return monomind.Event{Type: monomind.EventUsage, HasCostUSD: true, CostUSD: usd}
}

func spendNear(a, b float64) bool { d := a - b; return d > -0.0001 && d < 0.0001 }

// A worker that streams its cost is charged to the session while it runs: the 90 percent
// warning and the 100 percent halt fire before it finishes.
func TestMissionSpendIsRecordedWhileTheWorkerRuns(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	step := make(chan struct{})
	r.ex.onRun = func(ctx context.Context, id int64, on func(monomind.Event)) {
		on(usageCost(9.2))
		select {
		case <-step:
		case <-ctx.Done():
			return
		}
		on(usageCost(10))
		<-ctx.Done()
	}
	r.next(r.c)

	eventually(t, "the 90 percent warning mid-worker", func() bool { return countNotices(r.em, "mission.budget_90") == 1 })
	if got := r.session().SpentUSD; !spendNear(got, 9.2) || r.session().State != tasks.SessionRunning {
		t.Fatalf("mid-worker: spent %v, state %s", got, r.session().State)
	}
	if atomic.LoadInt32(&r.ex.running) != 1 {
		t.Fatal("the worker should still be running")
	}

	close(step)
	eventually(t, "the halt mid-worker", func() bool { return r.session().State == tasks.SessionPaused && atomic.LoadInt32(&r.ex.running) == 0 })
	if got := r.session().SpentUSD; !spendNear(got, 10) {
		t.Fatalf("spent %v after the halt: the worker's cost is counted once", got)
	}
	if n := countNotices(r.em, "mission.budget_halt"); n != 1 {
		t.Fatalf("halt notices %d", n)
	}
	r.waitStatus(a.ID, tasks.StatusReady)
	if n := countNotices(r.em, "mission.budget_90"); n != 1 {
		t.Fatalf("warnings %d", n)
	}
}

// What a worker streamed and what it reports at the end are one cost, not two.
func TestMissionStreamedCostIsNotCountedAgainAtTheEnd(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.ex.cost = 0.4 // the result's cost
	r.ex.onRun = func(ctx context.Context, id int64, on func(monomind.Event)) { on(usageCost(0.4)) }
	r.next(r.c)
	eventually(t, "the streamed spend", func() bool { return spendNear(r.session().SpentUSD, 0.4) })
	r.ex.release(a.ID)
	r.waitStatus(a.ID, tasks.StatusReview)
	eventually(t, "the worker to end", func() bool { return r.c.taskWorker(a.ID) == nil })
	if got := r.session().SpentUSD; !spendNear(got, 0.4) {
		t.Fatalf("spent %v, want 0.4 once", got)
	}
}

// The watcher applies the 90 percent warning to a running worker on its own tick.
func TestMissionTickWarnsAtNinetyPercentWhileAWorkerRuns(t *testing.T) {
	r := newRig(t, rigOpts{budget: 10})
	r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	if _, err := r.store.AddSessionSpend(tbg, "default", r.sid, 9.5, "other spend", operator); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the warning from the tick", func() bool { return countNotices(r.em, "mission.budget_90") == 1 })
}

// A failed persistence attempt must remain uncharged in memory so the next
// report can retry it. Concurrent reports must not charge that retry twice.
func TestMissionSpendRetriesFailedPersistenceWithoutDoubleCharging(t *testing.T) {
	db := openMissionDB(t)
	store := tasks.NewStore(db)
	se, err := store.CreateSession(tbg, "default", tasks.SessionInput{Title: "spend retry", BudgetUSD: 10}, operator)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.AddSessionTask(tbg, "default", se.ID, tasks.SessionTaskInput{Title: "work", Kind: tasks.KindTask, Acceptance: "rubric: work is complete"}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveSession(tbg, "default", se.ID, operator); err != nil {
		t.Fatal(err)
	}
	mc := &MissionRun{Store: store, ProfileID: "default", Session: se.ID}
	c := New(tbg, Config{Missions: mc})
	t.Cleanup(c.Close)
	w := &worker{id: "w1", cost: 0.5, task: &missionBind{session: se.ID, task: task}}
	if _, err := db.Exec(`CREATE TRIGGER fail_session_spend BEFORE UPDATE OF spent_usd ON task_sessions BEGIN SELECT RAISE(ABORT, 'transient persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if c.recordSpend(tbg, mc, w) {
		t.Fatal("failed write was reported as charged")
	}
	if w.task.spent != 0 {
		t.Fatalf("failed write advanced charged amount to %v", w.task.spent)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_session_spend`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.recordSpend(tbg, mc, w) }()
	}
	wg.Wait()
	got, err := store.GetSession(tbg, "default", se.ID, operator)
	if err != nil || !spendNear(got.SpentUSD, 0.5) || !spendNear(w.task.spent, 0.5) {
		t.Fatalf("retry must charge once: session %+v, charged %v, error %v", got, w.task.spent, err)
	}
	w.cost = 0.8
	if !c.recordSpend(tbg, mc, w) {
		t.Fatal("later incremental charge missing")
	}
	got, err = store.GetSession(tbg, "default", se.ID, operator)
	if err != nil || !spendNear(got.SpentUSD, 0.8) {
		t.Fatalf("incremental charge: %+v %v", got, err)
	}
}
