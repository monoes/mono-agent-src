package coderhost

import "testing"

func queueMission(t *testing.T, st *Store, conv, turn string) Command {
	t.Helper()
	c, err := st.Enqueue(Command{ProfileID: "p", ConversationID: conv, Kind: KindSend, TurnID: turn, Message: "go", WantsMission: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func setSeam[T any](t *testing.T, p *T, v T) {
	t.Helper()
	prev := *p
	t.Cleanup(func() { *p = prev })
	*p = v
}

// A confirmation that runs entirely in the middle of host.end (after one of its
// two steps, before the other) must leave no record: the command is already
// failed when its check runs, or the revoke that follows finds the record.
func TestConfirmInTheMiddleOfEndLeavesNoRecord(t *testing.T) {
	h, _, st := newHost(t)
	c := queueMission(t, st, "c", "t1")
	var confirmErr error
	setSeam(t, &endBetweenSteps, func() { _, confirmErr = st.Confirm(c, 0) })
	h.end(c, CmdFailed, "x")
	if confirmErr == nil {
		t.Fatal("a confirmation was written in the middle of end, for a command still pending")
	}
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) survive a failed command", n)
	}
}

// end that lands between Confirm's insert and its recheck leaves no record.
func TestEndBetweenConfirmInsertAndRecheckLeavesNoRecord(t *testing.T) {
	h, _, st := newHost(t)
	c := queueMission(t, st, "c", "t1")
	setSeam(t, &confirmAfterInsert, func() { h.end(c, CmdFailed, "x") })
	if _, err := st.Confirm(c, 0); err == nil {
		t.Fatal("a command failed during the confirmation was confirmed")
	}
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) left", n)
	}
}

// end that completes before Confirm starts: Confirm refuses and writes nothing.
func TestConfirmAfterEndRefuses(t *testing.T) {
	h, _, st := newHost(t)
	c := queueMission(t, st, "c", "t1")
	h.end(c, CmdFailed, "x")
	if _, err := st.Confirm(c, 0); err == nil {
		t.Fatal("a failed command was confirmed")
	}
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) left", n)
	}
}

// The peek refuses the record of a command that is not pending any more.
func TestPeekRefusesARecordOfACommandThatIsNotPending(t *testing.T) {
	_, _, st := newHost(t)
	c, _ := sendMission(t, st, "c", "t1")
	if _, ok := st.PeekConfirm(c); !ok {
		t.Fatal("setup: no live record")
	}
	if err := st.SetCommand(c.ID, CmdDone, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.PeekConfirm(c); ok {
		t.Fatal("a done command's record was offered to start a turn")
	}
}

// A child that admitted itself may consume after the host marked the command
// done: the consume does not look at the status.
func TestConsumeStillWorksOnceTheCommandIsDone(t *testing.T) {
	_, _, st := newHost(t)
	c, mc := sendMission(t, st, "c", "t1")
	if err := st.SetCommand(c.ID, CmdDone, ""); err != nil {
		t.Fatal(err)
	}
	if !st.ConsumeConfirm("p", "c", "t1", mc.Token(), 0) {
		t.Fatal("a starting child was cut off after its command was marked done")
	}
}

// The sweep keeps the record of a done command and drops the records of every
// other command that is not pending (the status column admits only those three).
func TestSweepKeepsDoneAndDropsOtherNonPending(t *testing.T) {
	_, _, st := newHost(t)
	pending, _ := sendMission(t, st, "c", "t1")
	done, _ := sendMission(t, st, "c", "t2")
	failed, _ := sendMission(t, st, "c", "t3")
	if err := st.SetCommand(done.ID, CmdDone, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCommand(failed.ID, CmdFailed, "x"); err != nil {
		t.Fatal(err)
	}
	st.sweepConfirms()
	if n := confirmRows(t, st); n != 2 {
		t.Fatalf("records left = %d, want the pending and the done one", n)
	}
	confirmFor(t, st, pending, "c", "t1")
	var left int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM configs WHERE name = ?`, confirmName("p", "c", "t3", failed.ID)).Scan(&left); err != nil || left != 0 {
		t.Fatalf("the record of the failed command is still there (%d, %v)", left, err)
	}
}

// Revoking after a status update also drops the record of a command that was
// confirmed after its ids were read.
func TestRevokeCommandsSweepsStragglers(t *testing.T) {
	_, _, st := newHost(t)
	c, _ := sendMission(t, st, "c", "t1")
	if err := st.SetCommand(c.ID, CmdFailed, "x"); err != nil {
		t.Fatal(err)
	}
	st.revokeCommands(nil)
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) left for a failed command", n)
	}
}
