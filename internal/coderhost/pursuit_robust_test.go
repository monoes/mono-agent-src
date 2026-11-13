package coderhost

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
)

// wakeable is the world the wake-up can be delivered in: the lead idle, unattended, with open work (newPursuitWorld adds
// one open task). Each case below breaks exactly one of those conditions.

func TestCheckInsKeepWorkingWhenThePursuitLoopIsNotWired(t *testing.T) {
	for _, wired := range []bool{false, true} {
		w := newPursuitWorld(t) // a started pursuit
		if !wired {
			w.h.Pursuit = nil
		}
		pursuitCheckinFixture(t, w, false)
		for i := 0; i < 4; i++ {
			w.advance(31 * time.Minute)
			w.h.checkinPass(w.clk.Now())
		}
		got := checkins(t, w.st)
		if !wired && len(got) == 0 {
			t.Fatal("a started pursuit with no loop wired left the lead without check-ins")
		}
		if wired && len(got) != 0 {
			t.Fatalf("check-ins woke a mission the loop serves: %+v", got)
		}
	}
}

func TestNoEpochIsOpenedThatCannotBeDelivered(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(w *pworld)
	}{
		{"the session is bound to no chat", func(w *pworld) { w.exec(`UPDATE task_sessions SET conversation_id = NULL WHERE id = 77`) }},
		{"outside the lead's active hours", func(w *pworld) {
			now := w.clk.Now().Local().Add(31*time.Minute + 3*time.Hour)
			setSettings(w.t, w.st, "m", `{"active_hours": "`+now.Format("15:04")+"-"+now.Add(time.Minute).Format("15:04")+`"}`)
		}},
		{"the lead is in a turn", func(w *pworld) { w.setLead("running", "turn-busy") }},
		{"a message waits for the lead", func(w *pworld) { send(w.t, w.st, "m", "turn-person", "hello") }},
		{"the lead is not unattended", func(w *pworld) {
			w.exec(`UPDATE coder_sessions SET unattended = 0 WHERE conversation_id = 'm'`)
		}},
		{"the mission has no open work", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'done', verified = 1 WHERE session_id = 77`)
			w.propose() // the lead has proposed the close: nothing is left to ask of it
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			c.break_(w)
			for i := 0; i < 3; i++ {
				w.pass()
				w.advance(31 * time.Minute)
			}
			w.pass()
			if w.model.count() != 0 || len(w.epochs()) != 0 || len(w.wakes()) != 0 {
				t.Fatalf("judge calls %d, epochs %v, wake-ups %d: the judge was paid for a wake-up that could not be delivered", w.model.count(), w.epochs(), len(w.wakes()))
			}
		})
	}
}

func TestOneUndeliveredEpochIsEnoughUntilItIsWoken(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	// the wake-up is queued but never ends (the lead is in a turn when it comes up); the timer is due again and again
	w.setLead("running", "turn-busy")
	for i := 0; i < 3; i++ {
		w.advance(31 * time.Minute)
		w.pass()
	}
	if w.model.count() != 1 || len(w.epochs()) != 1 {
		t.Fatalf("judge calls %d, epochs %v: an epoch was opened while the lead was busy", w.model.count(), w.epochs())
	}
}

func TestAMissionWithoutOpenWorkWhenTheEpochEndsGetsNoWakeUp(t *testing.T) {
	w := newPursuitWorld(t)
	w.model.onCall = func() { // the work ends during the judge call and the lead has proposed the close
		w.exec(`UPDATE tasks SET status = 'done', verified = 1 WHERE session_id = 77`)
		w.propose()
	}
	w.firstEpoch()
	if len(w.epochs()) != 1 || len(w.wakes()) != 0 {
		t.Fatalf("epochs %v wake-ups %d", w.epochs(), len(w.wakes()))
	}
}

func lostWakeFixture(t *testing.T) (*pworld, int64) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	got := w.wakes()
	if len(got) != 1 {
		t.Fatalf("wake-ups = %d", len(got))
	}
	return w, got[0].ID
}

func (w *pworld) endCommand(id int64, status string) {
	w.exec(`UPDATE coder_session_commands SET status = ?, error = 'ended' WHERE id = ?`, status, id)
}

func TestAWakeUpThatEndedWithoutRunningIsQueuedAgainABoundedNumberOfTimes(t *testing.T) {
	w, id := lostWakeFixture(t)
	for round := 1; round <= maxWakeTries+2; round++ {
		w.endCommand(id, CmdFailed)
		w.pass()
		got := w.wakes()
		if round < maxWakeTries {
			if len(got) != 1 || got[0].ID == id {
				t.Fatalf("round %d: the lost wake-up was not queued again: %+v", round, got)
			}
			id = got[0].ID
		} else if len(got) != 0 {
			t.Fatalf("round %d: tried again beyond the bound: %+v", round, got)
		}
	}
	if w.model.count() != 1 || len(w.epochs()) != 1 {
		t.Fatal("a lost wake-up cost another epoch")
	}
}

func TestAWakeUpThatRanIsNotQueuedAgain(t *testing.T) {
	w, id := lostWakeFixture(t)
	w.endCommand(id, CmdDone)
	w.pass()
	if len(w.wakes()) != 0 {
		t.Fatal("a wake-up that ran was queued again")
	}
}

func TestAPursuitWakeUpExpiresLikeACheckIn(t *testing.T) {
	w, id := lostWakeFixture(t)
	w.advance(MaxCheckinAge - time.Minute)
	if err := w.st.ExpireSends(); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := w.st.GetCommand(id); c.Status != CmdPending {
		t.Fatalf("expired early: %+v", c)
	}
	w.advance(2 * time.Minute)
	if err := w.st.ExpireSends(); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := w.st.GetCommand(id); c.Status != CmdFailed {
		t.Fatalf("a stale digest is still waiting to start a turn: %+v", c)
	}
}

func TestAParkedEngineIsRecordedForThePersonAndLoggedOnce(t *testing.T) {
	w := newPursuitWorld(t)
	w.pass()
	var logged []string
	w.h.Logf = func(f string, a ...any) { logged = append(logged, f) }
	m := pursuitMission{ID: pursuitSession, Profile: "p", Conv: "m"}
	run, err := w.h.pursuitRunOf(m, goalOf(w))
	if err != nil {
		t.Fatal(err)
	}
	parked := epoch.TickResult{Outcome: epoch.OutcomeInert, Held: epoch.HeldParked}
	w.h.noteEngineResult(m, run, parked, nil)
	w.h.noteEngineResult(m, run, parked, nil)
	if len(logged) != 1 {
		t.Fatalf("logged %d times, want once", len(logged))
	}
	w.pass() // the gate now shows it
	if got := w.waiting(); got != "engine-parked" {
		t.Fatalf("waiting = %q", got)
	}
	w.advance(5 * time.Hour)
	w.pass()
	if w.model.count() != 0 {
		t.Fatal("a parked engine spent")
	}
}

func TestThePanicBackoffHoldsAndThenLetsTheMissionRunAgain(t *testing.T) {
	w := newPursuitWorld(t)
	boom := true
	w.h.Pursuit.Clock = panicClock{inner: w.clk, when: func() bool { return boom }}
	w.h.Logf = func(string, ...any) {}
	w.pass() // panics, backs off
	boom = false
	w.pass() // inside the back-off: nothing
	var n int
	_ = w.st.db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit_state`).Scan(&n)
	if n != 0 {
		t.Fatal("the mission was served inside its back-off")
	}
	w.advance(pursuitPanicBackoff + time.Second)
	w.pass()
	_ = w.st.db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit_state`).Scan(&n)
	if n != 1 {
		t.Fatal("the mission was not served after its back-off")
	}
	if len(w.h.pursuitBackoff) != 0 {
		t.Fatalf("expired back-offs are kept: %v", w.h.pursuitBackoff)
	}
}

func TestNoJudgeCallInsideTheEpochFloor(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	w.setLead("idle", "")
	w.endCommandsDone()
	for i := 0; i < 5; i++ {
		w.advance(time.Minute)
		w.pass()
	}
	if w.model.count() != 1 {
		t.Fatalf("judge calls = %d", w.model.count())
	}
}

func (w *pworld) endCommandsDone() {
	w.exec(`UPDATE coder_session_commands SET status = 'done' WHERE status = 'pending'`)
}

func TestALostWakeUpIsRetriedBeforeAnyNewEpochOpens(t *testing.T) {
	w, id := lostWakeFixture(t)
	w.endCommand(id, CmdFailed)
	w.advance(31 * time.Minute)
	w.pass()
	if len(w.epochs()) != 1 || w.model.count() != 1 || len(w.wakes()) != 1 {
		t.Fatalf("epochs %v, judge calls %d, wake-ups %d: the owed wake-up comes before a new epoch", w.epochs(), w.model.count(), len(w.wakes()))
	}
}
