package coderhost

import (
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
