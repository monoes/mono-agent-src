package chat

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// heldTurn runs one chat turn while the test acts at each of its gates, in order, and returns what the mission's
// meter and the conversation's spend say after it.
func heldTurn(r leadSpendRig, id string, atGate ...func()) (meter, conversation float64) {
	type spend struct{ meter, conversation float64 }
	done := make(chan spend, 1)
	go func() {
		var s spend
		defer func() { done <- s }() // also when the turn fails the test
		s.meter, s.conversation = r.turn(id)
	}()
	for _, act := range atGate {
		r.atGate()
		act()
		r.release()
	}
	s := <-done
	return s.meter, s.conversation
}

func wantMeter(t *testing.T, r leadSpendRig, when string, usd float64, state tasks.SessionState) {
	t.Helper()
	meter, got := r.meter()
	if math.Abs(meter-usd) > 1e-9 || got != state {
		t.Errorf("%s: the mission's meter says $%v and it is %s, want $%v and %s", when, meter, got, usd, state)
	}
}

var leadOperator = tasks.Actor{Kind: tasks.Human}

// A mission paused while the lead's turn runs (a worker's spend reached the budget, or the operator's pause) is
// still charged for that turn: before the workers are closed and the session compacted, which then adds only its own
// part.
func TestMissionLeadTurnIsChargedToAMissionPausedMidTurn(t *testing.T) {
	r := newLeadSpendRig(t, "0.41 warn hold\n0.5 - hold\n", true)
	meter, conv := heldTurn(r, "t1", func() {
		if _, err := r.store.PauseSession(context.Background(), "default", r.session, leadOperator); err != nil {
			t.Error(err)
		}
	}, func() {
		wantMeter(t, r, "while the turn's session is compacted", 0.41, tasks.SessionPaused)
	})
	wantSpend(t, "the paused turn", meter, conv, 0.5)
}

// A mission the lead's turn itself takes to its budget is halted by that charge, and is still charged what the
// compaction adds: also when the mission was approved only after the turn began.
func TestMissionHaltedByTheLeadTurnIsChargedItsCompaction(t *testing.T) {
	r := newLeadSpendRig(t, "6 warn hold\n6.5\n", false)
	meter, conv := heldTurn(r, "t1", func() {
		if _, err := r.store.ApproveSession(context.Background(), "default", r.session, leadOperator); err != nil {
			t.Error(err)
		}
	})
	wantSpend(t, "the turn that spent the budget", meter, conv, 6.5)
	wantMeter(t, r, "after it", 6.5, tasks.SessionPaused)
}

// A closed session and a draft are never charged; neither is a mission that was already paused when the turn began.
func TestMissionLeadTurnIsNotCharged(t *testing.T) {
	ctx := context.Background()
	t.Run("stopped mid-turn", func(t *testing.T) {
		r := newLeadSpendRig(t, "0.41 warn hold\n0.5\n", true)
		heldTurn(r, "t1", func() {
			if _, err := r.store.StopSession(ctx, "default", r.session, leadOperator); err != nil {
				t.Error(err)
			}
		})
		wantMeter(t, r, "the stopped mission", 0, tasks.SessionStopped)
	})
	t.Run("draft", func(t *testing.T) {
		r := newLeadSpendRig(t, "0.41 warn\n0.5\n", false)
		r.turn("t1")
		wantMeter(t, r, "the draft", 0, tasks.SessionDraft)
	})
	t.Run("paused before the turn", func(t *testing.T) {
		r := newLeadSpendRig(t, "0.41 warn\n0.5\n", true)
		if _, err := r.store.PauseSession(ctx, "default", r.session, leadOperator); err != nil {
			t.Fatal(err)
		}
		r.turn("t1")
		wantMeter(t, r, "the paused mission", 0, tasks.SessionPaused)
	})
}

// unreadable is a journal that can't be read.
type unreadable struct{}

func (unreadable) Query(string, ...any) (*sql.Rows, error) { return nil, errors.New("no journal") }
func (unreadable) QueryRow(string, ...any) *sql.Row        { return nil }

// Once part of a turn is charged, the exec's own figure (it holds that part) is never added on top of it.
func TestLeadTurnOwedNeverChargesTheExecFigureTwice(t *testing.T) {
	res := &monomind.TurnResult{CostUSD: 0.9, HasCostUSD: true}
	journal := &turnJournal{profileID: "default", conversationID: "c", turnID: "t"}
	if got := leadTurnOwed(unreadable{}, journal, res, 0); got != 0.9 {
		t.Errorf("nothing charged yet: owed $%v, want the exec's $0.9", got)
	}
	if got := leadTurnOwed(unreadable{}, journal, res, 0.41); got > 0 {
		t.Errorf("$0.41 already charged: owed $%v, want nothing", got)
	}
}
