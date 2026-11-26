package coderhost

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The wrap-up allowance of a spent mission is the person's: nothing the host does by itself may draw on it (#1751).

// spendMission gives the session a budget of $1 and spends it.
func spendMission(t *testing.T, ts *tasks.Store, se tasks.Session) {
	t.Helper()
	budget := 1.0
	if _, err := ts.UpdateSession(context.Background(), "p", se.ID, tasks.SessionUpdate{BudgetUSD: &budget}, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.AddSessionSpend(context.Background(), "p", se.ID, 2, "workers", opActor); err != nil {
		t.Fatal(err)
	}
}

func raiseBudget(t *testing.T, ts *tasks.Store, se tasks.Session) {
	t.Helper()
	budget := 10.0
	if _, err := ts.UpdateSession(context.Background(), "p", se.ID, tasks.SessionUpdate{BudgetUSD: &budget}, opActor); err != nil {
		t.Fatal(err)
	}
}

func TestNoCheckInIsScheduledForASpentMission(t *testing.T) {
	for name, tc := range map[string]struct {
		children int
		pause    bool
	}{
		"a lead without children":        {},
		"a master with children":         {children: 1},
		"a master with a paused mission": {children: 1, pause: true},
	} {
		t.Run(name, func(t *testing.T) {
			h, _, st := newHost(t)
			var se tasks.Session
			var ts *tasks.Store
			if tc.children > 0 {
				family(t, st, tc.children)
				se, ts = missionOfMaster(t, st, true)
			} else {
				se, ts = leadOf(t, st, true)
			}
			h.checkinPass(t0)
			finishFirstTask(t, ts, se)
			spendMission(t, ts, se)
			if tc.pause {
				if _, err := ts.PauseSession(context.Background(), "p", se.ID, opActor); err != nil {
					t.Fatal(err)
				}
			}
			if tc.children > 0 {
				addTurn(t, st, "k1", "k1-t1", "completed", "work", t0.Add(time.Minute)) // an event trigger too
			}
			h.checkinPass(t0.Add(32 * time.Minute))
			if got := checkins(t, st); len(got) != 0 {
				t.Fatalf("a spent mission was checked in on: %+v", got)
			}
			// raised (and running again): the same change is now worth a check-in
			raiseBudget(t, ts, se)
			if tc.pause {
				if _, err := ts.ResumeSession(context.Background(), "p", se.ID, opActor); err != nil {
					t.Fatal(err)
				}
			}
			h.checkinPass(t0.Add(48 * time.Minute))
			if got := checkins(t, st); len(got) != 1 {
				t.Fatalf("check-ins after the raise: %+v", got)
			}
		})
	}
}

// Whatever was queued before the budget ran out, or is queued by an agent afterwards, does not run; what the person
// sends does.
func TestOnlyThePersonsMessageRunsOnASpentMission(t *testing.T) {
	for _, tc := range []struct {
		turn, issuer, conv string
		runs               bool
	}{
		{turn: "checkin-1", conv: "m"},
		{turn: "answer-m-1", conv: "m"},
		{turn: "answer-d-1", conv: "m"},
		{turn: "ask-q-1", conv: "m"},
		{turn: "ask-r-1", conv: "m"},
		{turn: "master-1", issuer: "m", conv: "k1"},
		{turn: "t-from-a-master", issuer: "m", conv: "k1"},
		{turn: "t-1", conv: "m", runs: true},
		{turn: "answer-p-1", conv: "m", runs: true},
		{turn: "ask-p-1", conv: "m", runs: true},
	} {
		t.Run(tc.turn, func(t *testing.T) {
			h, l, st := newHost(t)
			family(t, st, 1)
			se, ts := missionFor(t, st, tc.conv, true)
			c, err := st.Enqueue(Command{ProfileID: "p", ConversationID: tc.conv, Kind: KindSend, TurnID: tc.turn, Message: "go", Issuer: tc.issuer,
				person: reservedPersonTurn(tc.turn)})
			if err != nil {
				t.Fatal(err)
			}
			spendMission(t, ts, se)
			if err := h.Tick(); err != nil {
				t.Fatal(err)
			}
			got, _, err := st.GetCommand(c.ID)
			if err != nil {
				t.Fatal(err)
			}
			l.mu.Lock()
			started := len(l.started)
			l.mu.Unlock()
			if tc.runs {
				if got.Status != CmdDone || started != 1 {
					t.Fatalf("the person's message: %s %q, %d started", got.Status, got.Error, started)
				}
				return
			}
			if got.Status != CmdFailed || !strings.Contains(got.Error, "wrap-up") || started != 0 {
				t.Fatalf("an automatic turn: %s %q, %d started", got.Status, got.Error, started)
			}
		})
	}
}

func TestAutomaticTurnTellsThePersonsTurnsApart(t *testing.T) {
	_, _, st := newHost(t)
	family(t, st, 1)
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "k1", Kind: KindSend, TurnID: "own-id", Message: "go", Issuer: "m"}); err != nil {
		t.Fatal(err)
	}
	for turn, want := range map[string]bool{
		"checkin-1": true, "master-1": true, "answer-m-1": true, "answer-d-1": true, "ask-q-1": true, "ask-r-1": true,
		"pursuit-1-a": true, "own-id": true,
		"t-1": false, "answer-p-1": false, "ask-p-1": false, "some-uuid": false,
	} {
		if got, err := st.AutomaticTurn("p", "k1", turn); err != nil || got != want {
			t.Errorf("%s: automatic %v %v, want %v", turn, got, err, want)
		}
	}
}

// A default is not applied while the mission is spent: its note could not run, and the question would be left for a
// person for good after the failed tries.
func TestNoDefaultIsAppliedOnASpentMission(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	se, ts := missionFor(t, st, "a", true)
	spendMission(t, ts, se)
	q := defaultable(t, st, clk, "a")
	if n, err := st.ApplyDefaults(); n != 0 || err != nil {
		t.Fatalf("applied %d, %v", n, err)
	}
	if got := questionOf(t, st, q.ID); got.Status != "open" {
		t.Fatalf("status = %s", got.Status)
	}
	raiseBudget(t, ts, se)
	if n, err := st.ApplyDefaults(); n != 1 || err != nil {
		t.Fatalf("after the raise: applied %d, %v", n, err)
	}
}

// The pursuit loop finds a spent mission's lead not deliverable, so it spends no judge call and queues no wake-up.
func TestPursuitDoesNotDeliverToASpentMission(t *testing.T) {
	h, _, st := newHost(t)
	se, ts := leadOf(t, st, true)
	m := pursuitMission{ID: se.ID, Profile: "p", Conv: "m"}
	if why := h.pursuitDeliverableFor(m, pursuitGateResult{}, true); why != "" {
		t.Fatalf("with money left: %s", why)
	}
	spendMission(t, ts, se)
	if why := h.pursuitDeliverableFor(m, pursuitGateResult{}, true); !strings.Contains(why, "wrap-up") {
		t.Fatalf("on a spent mission: %q", why)
	}
}
