package coderhost

import (
	"testing"
	"time"
)

// Settled work: a mission whose every task is done, or verified and waiting in review, has nothing for the lead to do, so
// the loop pays no judge call and queues no wake-up for it. Work the lead can still move (open, unblocked, not settled) does.

// settledSpend runs several timer periods of passes and returns what the loop spent: judge calls, epochs, wake-ups.
func (w *pworld) settledSpend(rounds int) (judges, epochs, wakes int) {
	w.t.Helper()
	for i := 0; i < rounds; i++ {
		w.pass()
		w.advance(31 * time.Minute)
	}
	w.pass()
	return w.model.count(), len(w.epochs()), len(w.wakes())
}

func TestSettledWorkPaysNoJudgeCallAndNoWake(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(w *pworld) // the world starts with one ready task
		spend bool
	}{
		{"every task verified in review", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'review', verified = 1 WHERE session_id = 77`)
			w.addTask(77, "review", 0, 1, 0)
			w.addTask(77, "done", 0, 1, 0)
		}, false},
		{"done and verified-in-review mixed, an epic still in progress", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'done' WHERE session_id = 77`)
			w.addTask(77, "review", 0, 1, 0)
			w.exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, kind, created_at, updated_at) VALUES ('p', 'epic', 'in_progress', 0, 77, 'epic', 'x', 'x')`)
		}, false},
		{"settled tasks with a parked question and an open direction", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'review', verified = 1 WHERE session_id = 77`)
			w.park(1)
			w.openDirection("d1", "a direction", 0)
		}, false},
		{"blocked work only", func(w *pworld) { w.exec(`UPDATE tasks SET blocked = 1 WHERE session_id = 77`) }, false},
		{"no tasks at all", func(w *pworld) { w.exec(`UPDATE tasks SET session_id = NULL WHERE session_id = 77`) }, false},
		{"verified in review but blocked", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'review', verified = 1, blocked = 1 WHERE session_id = 77`)
		}, false},
		{"open unblocked work", func(w *pworld) {}, true},
		{"review that is not verified", func(w *pworld) { w.exec(`UPDATE tasks SET status = 'review', verified = 0 WHERE session_id = 77`) }, true},
		{"verified but still in progress", func(w *pworld) { w.exec(`UPDATE tasks SET status = 'in_progress', verified = 1 WHERE session_id = 77`) }, true},
		{"one failed task beside settled ones", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'review', verified = 1 WHERE session_id = 77`)
			w.addTask(77, "ready", 0, 0, 3)
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			c.setup(w)
			judges, epochs, wakes := w.settledSpend(3)
			if c.spend && (judges == 0 || wakes == 0) {
				t.Fatalf("work the lead can move earned no spend: judge calls %d, epochs %d, wake-ups %d", judges, epochs, wakes)
			}
			if !c.spend && (judges != 0 || epochs != 0 || wakes != 0) {
				t.Fatalf("settled or blocked work was paid for: judge calls %d, epochs %d, wake-ups %d", judges, epochs, wakes)
			}
		})
	}
}

func TestTasksCreatedBetweenPassesEndTheSettledQuiet(t *testing.T) {
	w := newPursuitWorld(t)
	w.exec(`UPDATE tasks SET status = 'review', verified = 1 WHERE session_id = 77`)
	if j, e, k := w.settledSpend(2); j != 0 || e != 0 || k != 0 {
		t.Fatalf("settled mission spent: %d %d %d", j, e, k)
	}
	w.addTask(77, "ready", 0, 0, 0)
	w.advance(31 * time.Minute)
	w.pass()
	if w.model.count() != 1 || len(w.wakes()) != 1 {
		t.Fatalf("a task created after the quiet earned no judge call/wake: %d %d", w.model.count(), len(w.wakes()))
	}
}

func TestAQueuedWakeIsDroppedWhenTheTasksSettleBeforeItRuns(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	if len(w.wakes()) != 1 {
		t.Fatal("no wake-up queued")
	}
	w.exec(`UPDATE tasks SET status = 'review', verified = 1 WHERE session_id = 77`)
	_ = w.h.Tick()
	if w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix) || len(w.wakes()) != 0 {
		t.Fatal("a queued wake-up ran for a mission whose work had settled")
	}
}
