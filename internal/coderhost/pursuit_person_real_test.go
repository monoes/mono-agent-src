package coderhost

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

var human = tasks.Actor{Kind: tasks.Human}

// The host writes under the operator's label too. These tests use the real writers of the tasks store: what they record
// must never move the person's last action.

// backdatePursuit moves the pursuit row's stamp an hour into the past of the real clock: the tasks store stamps its events
// with the real clock, so without this they would fall in the same second as the start and could not be told apart.
func (w *pworld) backdatePursuit() {
	w.exec(`UPDATE mission_pursuit SET updated_at = ? WHERE session_id = 77`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))
}

func (w *pworld) personAt() time.Time {
	w.t.Helper()
	m := pursuitMission{ID: pursuitSession, Profile: "p", Conv: "m"}
	tree, err := w.st.missionTree(m)
	if err != nil {
		w.t.Fatal(err)
	}
	at, err := w.st.personActionAt(m, tree)
	if err != nil {
		w.t.Fatal(err)
	}
	return at
}

func TestHostAutomationUnderTheOperatorLabelIsNotAPersonAction(t *testing.T) {
	w := newPursuitWorld(t)
	w.backdatePursuit()
	before := w.personAt()
	ts := tasks.NewStore(w.st.db)
	w.advance(time.Minute)
	// the charge of an ended child and an in-turn worker's spend (AddSessionSpend / AddSessionSpendWith with a Human actor)
	if _, err := ts.AddSessionSpend(bg, "p", 77, 1.5, "child session of task #1", human); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddSessionSpendWith(bg, "p", 77, 0.5, "child session of task #2", human, func(context.Context, tasks.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// the conductor's close proposal and a note
	if err := ts.AddSessionEvent(bg, "p", 77, tasks.EventCloseProposed, "mark", human); err != nil {
		t.Fatal(err)
	}
	if err := ts.AddSessionEvent(bg, "p", 77, tasks.EventNote, "a note", human); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddSessionTask(bg, "p", 77, tasks.SessionTaskInput{Title: "t", Acceptance: "a"}, human); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = w.st.db.QueryRow(`SELECT COUNT(*) FROM task_session_events WHERE session_id = 77 AND actor = 'you'`).Scan(&n)
	if n < 4 {
		t.Fatalf("only %d events were written under the operator's label: the test proves nothing", n)
	}
	if got := w.personAt(); !got.Equal(before) {
		t.Fatalf("the host's own writes moved the person's last action: %v -> %v", before, got)
	}
}

func TestTheSpendTheLimitMeasuresDoesNotResetIt(t *testing.T) {
	w := newPursuitWorld(t)
	w.backdatePursuit()
	w.pass()
	w.park(3)
	w.advance(31 * time.Minute)
	w.pass()
	if w.waiting() != "parked" {
		t.Fatalf("waiting = %q", w.waiting())
	}
	ts := tasks.NewStore(w.st.db)
	w.advance(time.Minute)
	if _, err := ts.AddSessionSpend(bg, "p", 77, 1.5, "child session of task #1", human); err != nil {
		t.Fatal(err)
	}
	w.advance(2 * time.Second)
	w.pass()
	st, _, _ := w.st.pursuitStateOf(pursuitSession)
	if w.waiting() != "parked" || st.BaseSpentMicro != 0 || st.SpentAway != 1500000 || st.WakesSince != 0 {
		t.Fatalf("the spend reset the counters it measures: waiting %q base %d away %d", w.waiting(), st.BaseSpentMicro, st.SpentAway)
	}
}

func TestTheOperatorsApprovalIsAPersonAction(t *testing.T) {
	// a draft mission approved through the real writer
	h, _, st := newHost(t)
	se, ts := missionFor(t, st, "m2", false)
	st.db.Exec(`INSERT OR IGNORE INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (?, 0, '', 'x', 'x')`, se.ID)
	before := (&pworld{t: t, st: st, h: h}).personAtOf(se.ID, "m2")
	if _, err := ts.ApproveSession(bg, "p", se.ID, human); err != nil {
		t.Fatal(err)
	}
	after := (&pworld{t: t, st: st, h: h}).personAtOf(se.ID, "m2")
	if !after.After(before) {
		t.Fatalf("the approval was not seen: %v -> %v", before, after)
	}
}

func (w *pworld) personAtOf(session int64, conv string) time.Time {
	w.t.Helper()
	// a pursuit row is what personActionAt reads first
	_, _ = w.st.db.Exec(`INSERT OR IGNORE INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (?, 0, '', 'x', 'x')`, session)
	m := pursuitMission{ID: session, Profile: "p", Conv: conv}
	tree, err := w.st.missionTree(m)
	if err != nil {
		w.t.Fatal(err)
	}
	at, err := w.st.personActionAt(m, tree)
	if err != nil {
		w.t.Fatal(err)
	}
	return at
}

func TestAQueuedWakeUpIsCheckedAgainstTheStreakWhenItIsAboutToRun(t *testing.T) {
	w := newPursuitWorld(t)
	w.firstEpoch()
	if len(w.wakes()) != 1 {
		t.Fatal("no wake-up queued")
	}
	settings, _ := w.st.GetSettings("p", "m")
	for i := 0; i < settings.CheckinLimit+12; i++ {
		addTurn(t, w.st, "m", "checkin-late-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "completed", "x", w.clk.Now().Add(-time.Duration(100-i)*time.Second))
	}
	_ = w.h.Tick()
	if w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix) || len(w.wakes()) != 0 {
		t.Fatal("a wake-up ran with the lead's host-made turns in a row at the limit")
	}
}

func TestAQueuedWakeUpIsCheckedAgainstTheConditionsOfItsDeliveryWhenItIsAboutToRun(t *testing.T) {
	for _, c := range []struct {
		name   string
		break_ func(w *pworld)
	}{
		{"the active hours ended", func(w *pworld) {
			now := w.clk.Now().Local().Add(3 * time.Hour)
			setSettings(w.t, w.st, "m", `{"active_hours": "`+now.Format("15:04")+"-"+now.Add(time.Minute).Format("15:04")+`"}`)
		}},
		{"the lead is no longer unattended", func(w *pworld) {
			w.exec(`UPDATE coder_sessions SET unattended = 0 WHERE conversation_id = 'm'`)
		}},
		{"the mission has no open work any more", func(w *pworld) {
			w.exec(`UPDATE tasks SET status = 'done', verified = 1 WHERE session_id = 77`)
			w.propose() // the lead has proposed the close: nothing is left to ask of it
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.firstEpoch()
			if len(w.wakes()) != 1 {
				t.Fatal("no wake-up queued")
			}
			c.break_(w)
			_ = w.h.Tick()
			if w.h.L.(*fakeLauncher).startedWith(PursuitTurnPrefix) || len(w.wakes()) != 0 {
				t.Fatal("the queued wake-up ran after its conditions were gone")
			}
		})
	}
}

func (w *pworld) sendFromHost(turn, issuer string) {
	w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'm', 'send', ?, 'x', '[]', 'done', ?, ?, ?)`,
		turn, w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano), issuer)
}

// A model default (answer-d-) and a master's answer (answer-m-) are notes with an empty issuer or the master's: they are not the
// person. Only answer-p- and ask-p- are. Widening the person prefixes (answer-p- to answer-) fails here.
func TestOnlyThePersonsOwnAnswerNotesMoveThePersonActionTime(t *testing.T) {
	for _, c := range []struct {
		turn   string
		person bool
	}{
		{answerTurnID(answerFromDefault), false},
		{answerTurnID(answerFromMaster), false},
		{askTurnID("m"), false},
		{answerTurnID(answerFromPerson), true},
		{askTurnID(askFromPerson), true},
	} {
		t.Run(c.turn[:8], func(t *testing.T) {
			w := newPursuitWorld(t)
			w.backdatePursuit()
			before := w.personAt()
			w.advance(time.Minute)
			w.sendFromHost(c.turn, "")
			got := w.personAt()
			if moved := got.After(before); moved != c.person {
				t.Fatalf("%s: the person action time moved = %v, want %v", c.turn, moved, c.person)
			}
		})
	}
}

// What counts as the person is pinned: adding an event kind or a prefix is a decision for review, not a quiet edit. Read the
// comment above personEventKinds in pursuit_person.go before changing this list: a kind that host automation also writes under
// the operator's label would let the host reset the very counters the pursuit's limits are measured by.
func TestThePersonActionSourcesArePinned(t *testing.T) {
	if len(personEventKinds) != 1 || personEventKinds[0] != "approved" {
		t.Fatalf("personEventKinds = %v: only \"approved\" is a kind a person alone causes; see the comment on personEventKinds before adding one", personEventKinds)
	}
	if answerFromPerson != "p" || askFromPerson != "p" || AnswerPrefix != "answer-" || AskPrefix != "ask-" {
		t.Fatalf("the person's note prefixes changed (%q%q-, %q%q-): pursuit_person.go counts exactly these as the person", AnswerPrefix, answerFromPerson, AskPrefix, askFromPerson)
	}
}
