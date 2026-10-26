package coderhost

import (
	"testing"
	"time"
)

func confirmFor(t *testing.T, st *Store, c Command, conv, turn string) {
	t.Helper()
	if _, ok := st.PeekConfirm(c); !ok {
		t.Fatalf("no live record for %s/%s", conv, turn)
	}
}

// Sweeping the commands of deleted conversations revokes their records.
func TestSweepOrphansRevokesTheConfirmationsOfTheCommandsItRemoves(t *testing.T) {
	_, _, st := newHost(t)
	addConversation(t, st, "gone", "/w/gone")
	addConversation(t, st, "kept", "/w/kept")
	g, _ := sendMission(t, st, "gone", "t1")
	k, _ := sendMission(t, st, "kept", "t2")
	if confirmRows(t, st) != 2 {
		t.Fatal("setup")
	}
	if _, err := st.db.Exec(`DELETE FROM ai_chat_conversations WHERE id = 'gone'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SweepOrphans(); err != nil {
		t.Fatal(err)
	}
	if n := confirmRows(t, st); n != 1 {
		t.Fatalf("records left = %d, want only the kept conversation's", n)
	}
	if _, ok := st.PeekConfirm(g); ok {
		t.Fatal("the record of a swept command is still live")
	}
	confirmFor(t, st, k, "kept", "t2")
}

// A record whose command row is gone or ended unstarted is dropped by the sweep,
// whatever its expiry says.
func TestSweepConfirmsDropsTheRecordsOfCommandsThatNoLongerWait(t *testing.T) {
	_, _, st := newHost(t)
	a, _ := sendMission(t, st, "c", "t1")
	b, _ := sendMission(t, st, "c", "t2")
	keep, _ := sendMission(t, st, "c", "t3")
	if _, err := st.db.Exec(`DELETE FROM coder_session_commands WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCommand(b.ID, CmdFailed, "x"); err != nil {
		t.Fatal(err)
	}
	st.sweepConfirms()
	if n := confirmRows(t, st); n != 1 {
		t.Fatalf("records left = %d, want 1", n)
	}
	confirmFor(t, st, keep, "c", "t3")
}

// A record is bound to the command row it was written for: the same id on a row
// created at another time (an id a later row could reuse) is a different command.
func TestConfirmationDoesNotMatchADifferentCommandWithTheSameID(t *testing.T) {
	_, _, st := newHost(t)
	clock := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return clock }
	c, mc := sendMission(t, st, "c", "t1")
	// the row the id names is now another command: same id, same turn, created later
	if _, err := st.db.Exec(`UPDATE coder_session_commands SET created_at = ? WHERE id = ?`, clock.Add(time.Second).UTC().Format(timeLayout), c.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.PeekConfirm(c); ok {
		t.Fatal("the record matched a command created at another time")
	}
	if st.ConsumeConfirm("p", "c", "t1", mc.Token(), 0) {
		t.Fatal("the record was consumed for a command created at another time")
	}
}

// A cancel that lands between Confirm's check and its insert leaves no record.
func TestConfirmLeavesNoRecordWhenTheCommandIsCancelledBeforeTheInsert(t *testing.T) {
	_, _, st := newHost(t)
	c, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "c", Kind: KindSend, TurnID: "t1", Message: "go", WantsMission: true})
	if err != nil {
		t.Fatal(err)
	}
	prev := confirmBeforeInsert
	t.Cleanup(func() { confirmBeforeInsert = prev })
	confirmBeforeInsert = func() {
		if n, err := st.CancelPendingSends("p", "c", c.ID+1, "cancelled by a stop"); err != nil || n != 1 {
			t.Errorf("cancel: %d, %v", n, err)
		}
	}
	if _, err := st.Confirm(c, 0); err == nil {
		t.Fatal("a command that ended before the record was written was confirmed")
	}
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) left for a dead command", n)
	}
}
