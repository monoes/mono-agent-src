package coderhost

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// sixWakes runs n epochs, each woken and its wake-up run, with the lead free again after each.
func (w *pworld) wakeRounds(n int) {
	w.t.Helper()
	w.h.Pursuit.MaxEpochsPerHour = 12
	w.pass()
	for i := 0; i < n; i++ {
		w.advance(31 * time.Minute)
		w.pass()
		w.endCommandsDone()
		w.setLead("idle", "")
	}
}

func TestTheWakeCapSinceThePersonLastActedStopsThePursuitWhateverTheMeterSays(t *testing.T) {
	w := newPursuitWorld(t)
	w.wakeRounds(6)
	if len(w.epochs()) != 6 {
		t.Fatalf("epochs %v: below the cap the pursuit goes on", w.epochs())
	}
	w.advance(31 * time.Minute)
	w.pass()
	if len(w.epochs()) != 6 || w.model.count() != 6 || w.waiting() != "wakes" {
		t.Fatalf("epochs %v, judge calls %d, waiting %q: the seventh epoch must not open", w.epochs(), w.model.count(), w.waiting())
	}
	// the person acts: the count starts again
	w.advance(time.Second)
	w.resumeAfterPause()
	w.advance(31 * time.Minute)
	w.pass()
	if len(w.epochs()) != 7 || w.waiting() != "" {
		t.Fatalf("after the person acted: epochs %v waiting %q", w.epochs(), w.waiting())
	}
}

func (w *pworld) resumeAfterPause() {
	w.pause()
	w.advance(2 * time.Second)
	w.resume()
	w.advance(2 * time.Second)
}

func TestTheWakeCapIsTheMissionsOwnSetting(t *testing.T) {
	w := newPursuitWorldLimits(t, missionpursuit.AwayLimits{Wakes: 2})
	w.wakeRounds(3)
	if len(w.epochs()) != 2 || w.waiting() != "wakes" {
		t.Fatalf("epochs %v waiting %q: the mission's own cap of 2", w.epochs(), w.waiting())
	}
}

func TestTheHostWideHourlyWakeCapStopsEveryMission(t *testing.T) {
	w := newPursuitWorld(t)
	for i := 0; i < MaxPursuitWakesPerHour; i++ {
		w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'other', 'send', ?, 'x', '[]', 'done', ?, ?, '')`,
			fmt.Sprintf("pursuit-9-%d", i), w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
	}
	w.pass()
	w.advance(31 * time.Minute)
	w.pass()
	if w.model.count() != 0 || len(w.wakes()) != 0 {
		t.Fatalf("judge calls %d, wake-ups %d: the host-wide cap is used", w.model.count(), len(w.wakes()))
	}
	pr, err := missionpursuit.NewStore(w.st.db).GetProgress(bg, "p", pursuitSession)
	if err != nil || pr.HostWakes != MaxPursuitWakesPerHour || pr.HostFreesAt == "" || pr.HostWakesMax != 12 {
		t.Fatalf("the status does not show the cap: %+v %v", pr, err)
	}
	w.advance(40 * time.Minute) // the first of them leaves the hour
	w.pass()
	if w.model.count() != 1 {
		t.Fatalf("judge calls = %d: the cap frees with the hour", w.model.count())
	}
}

func TestHostMadeTurnsInARowStopTheWakeUpsLikeTheCheckIns(t *testing.T) {
	for _, many := range []bool{false, true} {
		w := newPursuitWorld(t)
		settings, _ := w.st.GetSettings("p", "m")
		n := 3
		if many {
			n = settings.CheckinLimit + 12
		}
		for i := 0; i < n; i++ {
			addTurn(t, w.st, "m", fmt.Sprintf("checkin-%d", i), "completed", "x", w.clk.Now().Add(-time.Duration(n-i)*time.Minute))
		}
		w.pass()
		w.advance(31 * time.Minute)
		w.pass()
		if many && (w.model.count() != 0 || len(w.wakes()) != 0) {
			t.Fatalf("judge calls %d wake-ups %d with %d host-made turns in a row", w.model.count(), len(w.wakes()), n)
		}
		if !many && len(w.wakes()) != 1 {
			t.Fatalf("the control did not wake: %d", len(w.wakes()))
		}
	}
}

func TestAQueuedWakeUpIsCheckedAgainstTheAwayLimitsWhenItIsAboutToRun(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	if len(w.wakes()) != 1 {
		t.Fatal("no wake-up queued")
	}
	w.park(3) // the limit is reached while the wake-up waits
	_ = w.h.Tick()
	for _, c := range w.wakes() {
		t.Fatalf("the wake-up is still pending: %+v", c)
	}
	if w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix) {
		t.Fatal("a wake-up ran after the away limit was reached")
	}
}

func TestALimitReachedDuringTheJudgeCallQueuesNoWakeUp(t *testing.T) {
	w := newPursuitWorld(t)
	w.model.onCall = func() { w.park(3) }
	w.firstEpoch()
	if len(w.epochs()) != 1 || len(w.wakes()) != 0 || w.waiting() != "parked" {
		t.Fatalf("epochs %v wake-ups %d waiting %q", w.epochs(), len(w.wakes()), w.waiting())
	}
}

func TestMorePersonActionsResetTheCounters(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(w *pworld)
	}{
		{"the person answers an ask", func(w *pworld) {
			w.exec(`INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, level, bytes, status, answered_by, answered_at, importance, question, created_at) VALUES ('p', 77, 'm', 0, 'root', 1, 'answered', 'person', ?, 'critical', 'q', ?)`,
				w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
		}},
		{"the person answers a plan (the note the operator's surface queues)", func(w *pworld) {
			if _, err := w.st.TellLeadAboutPlan(bg, "p", "m", 77, PlanAsked, "which?"); err != nil {
				w.t.Fatal(err)
			}
			w.endCommandsDone() // the note has been delivered
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.pass()
			w.park(3)
			w.advance(31 * time.Minute)
			w.pass()
			if w.waiting() != "parked" {
				t.Fatalf("waiting = %q", w.waiting())
			}
			w.advance(time.Minute)
			c.act(w)
			w.advance(2 * time.Second)
			w.pass()
			if w.waiting() != "" || len(w.epochs()) != 1 {
				t.Fatalf("waiting %q epochs %v", w.waiting(), w.epochs())
			}
		})
	}
}

func TestAnAgentsRecordsAreNotPersonActions(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(w *pworld)
	}{
		{"an ask answered by a lead", func(w *pworld) {
			w.exec(`INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, level, bytes, status, answered_by, answered_at, importance, question, created_at) VALUES ('p', 77, 'm', 0, 'root', 1, 'answered', 'master:k1', ?, 'critical', 'q', ?)`,
				w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.pass()
			w.park(3)
			w.advance(31 * time.Minute)
			w.pass()
			w.advance(time.Minute)
			c.act(w)
			w.advance(2 * time.Second)
			w.pass()
			if w.waiting() != "parked" {
				t.Fatalf("waiting = %q: an agent's record reset the counters", w.waiting())
			}
		})
	}
}

func TestTheStatusShowsTheLimitsAndWhyThePursuitWaits(t *testing.T) {
	w := newPursuitWorld(t)
	w.pass()
	w.park(3)
	w.advance(31 * time.Minute)
	w.setSpent(1.0)
	w.pass()
	pr, err := missionpursuit.NewStore(w.st.db).GetProgress(bg, "p", pursuitSession)
	if err != nil {
		t.Fatal(err)
	}
	if !pr.Seen || pr.Parked != 3 || pr.ParkedMax != 3 || pr.SpentAwayUSD != 1.0 || pr.BudgetUSD != 10 || pr.WakesMax != 6 || pr.Waiting != "parked" || !strings.Contains(pr.Why, "questions wait") {
		t.Fatalf("progress = %+v", pr)
	}
}
