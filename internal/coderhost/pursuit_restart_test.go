package coderhost

import (
	"context"
	"strings"
	"testing"
	"time"
)

// restart is a new host process over the same database: no engine, no in-memory state.
func (w *pworld) restart() {
	w.t.Helper()
	d := *w.h.Pursuit
	w.h = &Host{Store: w.st, L: w.h.L, Pursuit: &d}
}

func (w *pworld) setLead(state, turn string) {
	w.exec(`UPDATE coder_sessions SET state = ?, turn_id = ? WHERE profile_id = 'p' AND conversation_id = 'm'`, state, turn)
}

func TestARestartDoesNotOpenOrWakeTwice(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	if len(w.epochs()) != 1 || len(w.wakes()) != 1 {
		t.Fatalf("setup: %v %d", w.epochs(), len(w.wakes()))
	}
	for i := 0; i < 3; i++ {
		w.restart()
		w.pass()
		w.advance(time.Minute)
		w.pass()
	}
	if len(w.epochs()) != 1 || len(w.wakes()) != 1 || w.model.count() != 1 {
		t.Fatalf("a restart spent again: epochs %v, wake-ups %d, judge calls %d", w.epochs(), len(w.wakes()), w.model.count())
	}
}

func TestARestartWakesTheLeadForAnEpochItCouldNotWakeFor(t *testing.T) {
	w := newPursuitWorld(t)
	w.setLead("running", "turn-busy") // the lead is in a turn when the epoch lands
	w.firstEpoch()
	if got := w.epochs(); len(got) != 1 || got[0] != "1:applied" || len(w.wakes()) != 0 {
		t.Fatalf("epochs %v wake-ups %d", got, len(w.wakes()))
	}
	w.restart()
	w.setLead("idle", "")
	w.pass()
	w.pass()
	if len(w.wakes()) != 1 || w.model.count() != 1 {
		t.Fatalf("wake-ups %d, judge calls %d: the epoch must be woken for once and not judged again", len(w.wakes()), w.model.count())
	}
}

func TestARestartResumesAnEpochTheJudgeNeverFinished(t *testing.T) {
	w := newPursuitWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.model.onCall = cancel // the host stops while the judge is thinking
	w.pass()
	w.advance(31 * time.Minute)
	w.h.pursuitPass(ctx)
	if got := w.epochs(); len(got) != 1 || got[0] != "1:open" {
		t.Fatalf("epochs = %v", got)
	}
	w.model.onCall = nil
	w.restart()
	w.pass()
	if got := w.epochs(); len(got) != 1 || got[0] != "1:applied" {
		t.Fatalf("epochs = %v: the same epoch is finished, not a second one opened", got)
	}
	var judgments int
	_ = w.st.db.QueryRow(`SELECT COUNT(*) FROM mission_strategy_judgments`).Scan(&judgments)
	if judgments != 1 || len(w.wakes()) != 1 {
		t.Fatalf("judgments %d wake-ups %d", judgments, len(w.wakes()))
	}
}

func TestTheWakeUpIsQueuedOnceWhateverTheNumberOfTries(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	lead := pursuitMission{ID: pursuitSession, Profile: "p", Conv: "m"}
	c := Command{ProfileID: "p", ConversationID: "m", Kind: KindSend, TurnID: PursuitTurnPrefix + "again", Message: "again"}
	if _, err := w.st.EnqueueWithPursuit(c, lead.ID, 1); err == nil {
		t.Fatal("a second wake-up for the same epoch was queued")
	}
	if len(w.wakes()) != 1 {
		t.Fatalf("wake-ups = %d", len(w.wakes()))
	}
}

func TestTheLeadIsWokenOnlyWhenItIsFree(t *testing.T) {
	for _, c := range []struct {
		name string
		busy func(w *pworld)
	}{
		{"a turn is running", func(w *pworld) { w.setLead("running", "turn-busy") }},
		{"the lead is stopped", func(w *pworld) { w.setLead("stopped", "") }},
		{"a command is waiting for it", func(w *pworld) { send(w.t, w.st, "m", "turn-person", "hello") }},
		{"outside the lead's active hours", func(w *pworld) {
			now := w.clk.Now().Local().Add(31*time.Minute + 3*time.Hour)
			win := now.Format("15:04") + "-" + now.Add(time.Minute).Format("15:04")
			setSettings(w.t, w.st, "m", `{"active_hours": "`+win+`"}`)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			c.busy(w)
			w.firstEpoch()
			for _, x := range w.wakes() {
				t.Fatalf("woke a lead that was not free: %+v", x)
			}
		})
	}
}

func TestAWakeUpStartsAPlainHostMadeTurn(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if !w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix) {
		t.Fatal("the wake-up did not start a turn")
	}
	for _, c := range w.wakes() {
		t.Fatalf("still pending: %+v", c)
	}
	if !strings.HasPrefix(PursuitTurnPrefix, "pursuit-") || !HostMade(PursuitTurnPrefix+"x") {
		t.Fatal("a pursuit turn is not host-made")
	}
}
