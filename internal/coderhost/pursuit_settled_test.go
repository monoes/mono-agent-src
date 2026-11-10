package coderhost

import (
	"testing"
	"time"
)

// Settled work: a mission whose every task is done, or verified and waiting in review, has nothing for the lead to do but
// close it. The loop wakes the lead ONCE per settled state to do that (a close wake: one judge call, one wake-up), then pays
// nothing more until a task changes; with the close proposed, or the mission closed, it pays nothing at all. Work the lead
// can still move (open, unblocked, not settled) is paid for as always.

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

// touchTasks marks every task of the mission as changed now (the host reads the newest change time); propose records the
// lead's close proposal a second later, as mission_finish does.
func (w *pworld) touchTasks() {
	w.exec(`UPDATE tasks SET updated_at = ? WHERE session_id = 77`, w.clk.Now().UTC().Format(time.RFC3339Nano))
}

func (w *pworld) propose() {
	w.advance(time.Second)
	w.exec(`INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (77, ?, 'conductor', 'close_proposed', 'm')`, w.clk.Now().UTC().Format(time.RFC3339Nano))
}

// settle makes every task verified in review, changed now.
func (w *pworld) settle() {
	w.exec(`UPDATE tasks SET status = 'review', verified = 1 WHERE session_id = 77`)
	w.touchTasks()
}

// wakeRows counts the pursuit wake-ups ever queued, run or not.
func (w *pworld) wakeRows() int {
	w.t.Helper()
	var n int
	if err := w.st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE kind = 'send' AND substr(turn_id, 1, 8) = 'pursuit-'`).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// quietRounds runs timer periods, each wake-up having run before the next pass.
func (w *pworld) quietRounds(n int) {
	for i := 0; i < n; i++ {
		w.pass()
		w.endCommandsDone()
		w.advance(31 * time.Minute)
	}
	w.pass()
	w.endCommandsDone()
}

func TestSettledWorkWithItsCloseProposedPaysNoJudgeCallAndNoWake(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(w *pworld) // the world starts with one ready task
		spend bool
	}{
		{"every task verified in review", func(w *pworld) {
			w.addTask(77, "review", 0, 1, 0)
			w.addTask(77, "done", 0, 1, 0)
			w.settle()
			w.propose()
		}, false},
		{"done and verified-in-review mixed, an epic still in progress", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'done' WHERE session_id = 77`)
			w.addTask(77, "review", 0, 1, 0)
			w.exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, kind, created_at, updated_at) VALUES ('p', 'epic', 'in_progress', 0, 77, 'epic', 'x', 'x')`)
			w.touchTasks()
			w.propose()
		}, false},
		{"settled tasks with a parked question and an open direction", func(w *pworld) {
			w.settle()
			w.propose()
			w.park(1)
			w.openDirection("d1", "a direction", 0)
		}, false},
		{"the mission is closed", func(w *pworld) {
			w.settle()
			w.exec(`UPDATE task_sessions SET state = 'done' WHERE id = 77`)
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
			w.settle()
			w.addTask(77, "ready", 0, 0, 3)
			w.propose()
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
				t.Fatalf("settled, closed or blocked work was paid for: judge calls %d, epochs %d, wake-ups %d", judges, epochs, wakes)
			}
		})
	}
}

// A settled mission with no close proposal earns exactly one close wake (one epoch and its judge call, then the wake-up, all
// counted as always), and then nothing, however many timer periods pass.
func TestSettledWorkWithNoProposalEarnsExactlyOneCloseWake(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(w *pworld)
	}{
		{"verified in review", func(w *pworld) { w.settle() }},
		{"an epic only", func(w *pworld) {
			w.exec(`UPDATE tasks SET kind = 'epic', status = 'in_progress' WHERE session_id = 77`)
			w.touchTasks()
		}},
		{"done", func(w *pworld) { w.exec(`UPDATE tasks SET status = 'done' WHERE session_id = 77`); w.touchTasks() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.advance(time.Second)
			c.setup(w)
			w.advance(time.Second)
			w.quietRounds(5)
			if w.model.count() != 1 || len(w.epochs()) != 1 || w.wakeRows() != 1 {
				t.Fatalf("judge calls %d, epochs %v, wake-ups %d: want exactly one of each", w.model.count(), w.epochs(), w.wakeRows())
			}
		})
	}
}

func TestATaskChangeRearmsExactlyOneCloseWake(t *testing.T) {
	w := newPursuitWorld(t)
	w.advance(time.Second)
	w.settle()
	w.advance(time.Second)
	w.quietRounds(3)
	if w.wakeRows() != 1 {
		t.Fatalf("wake-ups = %d before the change", w.wakeRows())
	}
	w.advance(time.Second)
	w.exec(`UPDATE tasks SET status = 'ready', verified = 0 WHERE session_id = 77`) // reopened: the lead works on it
	w.touchTasks()
	w.advance(time.Second)
	w.settle() // and settles again
	w.advance(time.Second)
	w.quietRounds(4)
	if w.wakeRows() != 2 || w.model.count() != 2 {
		t.Fatalf("wake-ups %d, judge calls %d: want one more of each after the change", w.wakeRows(), w.model.count())
	}
}

func TestAStaleCloseProposalDoesNotQuietASettledStateThatChangedAfterIt(t *testing.T) {
	w := newPursuitWorld(t)
	w.advance(time.Second)
	w.settle()
	w.propose()
	w.advance(time.Second)
	w.settle() // a task changed after the proposal: the proposal rests on an older state
	w.advance(time.Second)
	w.quietRounds(3)
	if w.wakeRows() != 1 {
		t.Fatalf("wake-ups = %d, want the one close wake", w.wakeRows())
	}
}

func TestTasksCreatedBetweenPassesEndTheSettledQuiet(t *testing.T) {
	w := newPursuitWorld(t)
	w.advance(time.Second)
	w.settle()
	w.propose()
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

// The wake-up queued for the settled state is itself the one wake: it runs. A second one, with another already sent since the
// newest task change, is dropped. A queued wake-up never counts against itself.
func TestAQueuedWakeRunsForTheSettledStateOnceAndIsDroppedWhenAnotherWentOut(t *testing.T) {
	for _, c := range []struct {
		name string
		prep func(w *pworld)
		runs bool
	}{
		{"the tasks settled after the wake was queued", func(w *pworld) { w.advance(time.Second); w.settle() }, true},
		{"the tasks settled and another wake had gone out since", func(w *pworld) {
			w.advance(time.Second)
			w.settle()
			w.advance(time.Second)
			w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'm', 'send', 'pursuit-0-other', 'x', '[]', 'done', ?, ?, '')`,
				w.clk.Now().UTC().Format(time.RFC3339Nano), w.clk.Now().UTC().Format(time.RFC3339Nano))
		}, false},
		{"the tasks settled and the close was proposed", func(w *pworld) { w.advance(time.Second); w.settle(); w.propose() }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.firstEpoch()
			if len(w.wakes()) != 1 {
				t.Fatal("no wake-up queued")
			}
			c.prep(w)
			_ = w.h.Tick()
			if ran := w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix); ran != c.runs {
				t.Fatalf("the queued wake-up ran = %v, want %v", ran, c.runs)
			}
		})
	}
}

func TestAQueuedWakeIsDroppedWhenTheTasksSettleBeforeItRuns(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	if len(w.wakes()) != 1 {
		t.Fatal("no wake-up queued")
	}
	w.advance(time.Second)
	w.settle()
	w.propose() // the lead proposed the close in the meantime: the wake-up has nothing left to ask
	_ = w.h.Tick()
	if w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix) || len(w.wakes()) != 0 {
		t.Fatal("a queued wake-up ran for a mission whose work had settled and whose close was proposed")
	}
}
