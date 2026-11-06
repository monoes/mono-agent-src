package coderhost

import (
	"errors"
	"testing"
	"time"
)

// A Stop's notice is a promptless "system-" turn (ai.RecordSystemNotice): it is not work, so the readers that ask for a
// chat's newest turn or count its check-in streak look past it. Mutant tried: dropping the filter makes LastTurn return it.
func TestSystemNoticeTurnsAreNotTheNewestTurnNorABrokenStreak(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	addTurn(t, st, "a", "turn-1", "completed", "do it", t0)
	addTurn(t, st, "a", "system-0011", "completed", "", t0.Add(1*time.Second))
	if id, status := st.LastTurn("p", "a"); id != "turn-1" || status != "completed" {
		t.Fatalf("LastTurn = %q %q, want the work turn", id, status)
	}
	addTurn(t, st, "a", "checkin-x", "completed", "check in", t0.Add(2*time.Second))
	addTurn(t, st, "a", "system-0012", "completed", "", t0.Add(3*time.Second))
	if _, _, _, err := st.CheckinStreak("p", "a"); err != nil {
		t.Fatal(err)
	}
	// a person's turn that merely has a system-looking id but a prompt is still a turn
	addTurn(t, st, "a", "system-person", "completed", "hello", t0.Add(4*time.Second))
	if id, _ := st.LastTurn("p", "a"); id != "system-person" {
		t.Fatalf("a turn with a prompt was skipped: %q", id)
	}
}

// turnFloor is when a stop must be newer than: a Stop's notice written after a person's stop must not move it.
func TestTurnFloorIgnoresASystemNoticeTurn(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	addTurn(t, st, "a", "turn-1", "completed", "do it", t0)
	addTurn(t, st, "a", "system-0011", "completed", "", t0.Add(5*time.Second))
	if got := st.turnFloor("p", "a", ""); !got.Equal(t0) {
		t.Fatalf("turnFloor = %v, want %v", got, t0)
	}
}

// OWED STOP NOTICE: the flag a stop sets when its chat message could not be written is the operator's. A master's patch that
// names it is refused whole and changes nothing, a code outside the set cannot be stored, and the flag is never inherited by a
// child. Mutant tried: leaving owed_stop_notice out of OperatorOnly lets the patch through.
func TestAMasterCannotSetOrClearTheOwedStopNotice(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	for _, p := range []string{
		`{"owed_stop_notice":{"charter":"c","generation":1,"intent_at":5,"code":"plain","at":5}}`,
		`{"owed_stop_notice":null}`,
		`{"checkin_minutes":5,"owed_stop_notice":{"charter":"c","generation":1,"intent_at":5,"code":"plain","at":5}}`,
	} {
		if _, _, err := st.SetChild(m, a, []byte(p)); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if got, _ := st.GetSettings("p", a); got.OwedStopNotice != nil || got.CheckinMinutes == 5 {
		t.Fatalf("a refused patch changed the settings: %+v", got)
	}
	if _, _, err := DefaultSettings().Apply([]byte(`{"owed_stop_notice":{"charter":"c","generation":1,"intent_at":5,"code":"<b>x</b>","at":5}}`)); err == nil {
		t.Fatal("a code outside the set was accepted")
	}
	if len(TouchesOperatorOnly([]string{"owed_stop_notice"})) != 1 || oneOf("owed_stop_notice", AgentAdjustable) {
		t.Fatal("owed_stop_notice must be operator-only and not agent-adjustable")
	}
	parent := DefaultSettings()
	parent.OwedStopNotice = &OwedStopNotice{Charter: "c", Generation: 1, IntentAt: 5, Code: "plain", At: 5}
	if childSettings(parent, 1, 3).OwedStopNotice != nil {
		t.Fatal("a child inherited the flag")
	}
	next, _, err := parent.Apply([]byte(`{"checkin_minutes":7}`))
	if err != nil || next.OwedStopNotice == nil || next.OwedStopNotice == parent.OwedStopNotice {
		t.Fatalf("a patch must keep the flag in a copy of its own: %+v %v", next.OwedStopNotice, err)
	}
}
