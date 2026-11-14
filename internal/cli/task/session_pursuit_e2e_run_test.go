package task

import (
	"testing"
	"time"
)

// E2E-1: one mission, begun by the real verbs, then the real host loop. The whole programme in one run: the first epoch and its
// one wake, the second, the money stop, the person's pause and resume through the verbs, and the epoch that follows.
func TestAScriptedPursuitBegunByTheCLIRunsToTheMoneyStopAndOnAfterThePersonResumes(t *testing.T) {
	w := newE2EWorld(t)

	w.pass() // the engine starts; its timer is not due
	if len(w.epochs()) != 0 || w.model.count() != 0 {
		t.Fatalf("an epoch opened before its timer: %v, %d judge calls", w.epochs(), w.model.count())
	}
	w.advance(31 * time.Minute)
	w.pass()
	if got := w.epochs(); len(got) != 1 || got[0] != "1:applied" || w.model.count() != 1 {
		t.Fatalf("after the first timer: epochs %v, judge calls %d", got, w.model.count())
	}
	w.tick()
	w.tick() // the same wake is never started twice
	if w.wakeCount() != 1 || len(w.l.caps) != 1 || w.l.caps[0] != 2 {
		t.Fatalf("first wake: %d started, caps %v", w.wakeCount(), w.l.caps)
	}
	if p := w.status(); !p.Seen || p.WakesUsed != 1 || p.JudgeCalls != 1 || p.Waiting != "" {
		t.Fatalf("status after the first wake: %+v", p)
	}

	w.finishTurns()
	w.nextEpoch()
	w.tick()
	if got := w.epochs(); len(got) != 2 || w.wakeCount() != 2 {
		t.Fatalf("second epoch: %v, wakes %d", got, w.wakeCount())
	}

	// The money crosses 20% of the 20 USD budget: nothing new opens, however long it waits.
	w.finishTurns()
	w.setSpent(4.5)
	w.advance(31 * time.Minute)
	w.pass()
	if p := w.status(); p.Waiting != "spend" {
		t.Fatalf("status at the money stop: %+v", p)
	}
	calls := w.model.count()
	w.advance(6 * time.Hour)
	w.pass()
	w.tick()
	if len(w.epochs()) != 2 || w.model.count() != calls || w.wakeCount() != 2 {
		t.Fatalf("a pursuit at its money stop went on: epochs %v, judge calls %d (was %d), wakes %d", w.epochs(), w.model.count(), calls, w.wakeCount())
	}

	// The person pauses, then resumes, through the verbs: the counters start again and the loop goes on.
	if st := w.personActed("pause"); !st.Pursuit.Paused() {
		t.Fatalf("pause: %+v", st.Pursuit)
	}
	w.advance(time.Hour)
	w.pass()
	if len(w.epochs()) != 2 || w.model.count() != calls {
		t.Fatalf("a paused pursuit spent: epochs %v, judge calls %d", w.epochs(), w.model.count())
	}
	if st := w.personActed("resume"); st.Pursuit.Paused() {
		t.Fatalf("resume: %+v", st.Pursuit)
	}
	w.nextEpoch()
	if got := w.epochs(); len(got) != 3 || got[2] != "3:applied" || w.model.count() != calls+1 {
		t.Fatalf("after the person resumed: epochs %v, judge calls %d, status %+v", got, w.model.count(), w.status())
	}
	if p := w.status(); p.Waiting != "" {
		t.Fatalf("still waiting after the person acted: %+v", p)
	}
}
