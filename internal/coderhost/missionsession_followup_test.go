package coderhost

import "testing"

func TestTickMissionCheckErrorFailsOnlyThatSend(t *testing.T) {
	h, l, st := newHost(t)
	a := addSession(t, st, "p", "c", "running", "")
	bad := confirmedAt(t, st, "c", "t1", a)
	plain, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "d", Kind: KindSend, TurnID: "t2", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// The open-session read now errors (the table is moved aside).
	if _, err := st.db.Exec(`ALTER TABLE task_sessions RENAME TO task_sessions_aside`); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatalf("one failed check aborted the pass: %v", err)
	}
	if got := cmdOutcome(t, st, bad.ID); got.Status != CmdFailed || got.Error == "" {
		t.Fatalf("mission send = %s %q, want failed", got.Status, got.Error)
	}
	if _, started := tokenOf(l, "t1"); started {
		t.Fatal("a turn started for the failed mission send")
	}
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) left", n)
	}
	if got := cmdOutcome(t, st, plain.ID); got.Status != CmdDone {
		t.Fatalf("the other chat's send = %s %q, want done", got.Status, got.Error)
	}
}

func TestTickRefusesWhenTheBoundSessionIsNoLongerOpen(t *testing.T) {
	for _, tc := range []struct {
		name, state, archived, conv string
		other                       string // an open session added afterwards ("" = none)
	}{
		{name: "archived", state: "running", archived: "2026-10-26T00:00:00Z", conv: "c"},
		{name: "done", state: "done", conv: "c"},
		{name: "stopped", state: "stopped", conv: "c"},
		{name: "failed", state: "failed", conv: "c"},
		{name: "deleted (gone from the conversation)", state: "running", conv: "gone"},
		{name: "replaced by a newer open one", state: "done", conv: "c", other: "draft"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, l, st := newHost(t)
			a := addSession(t, st, "p", "c", "running", "")
			c := confirmedAt(t, st, "c", "t1", a)
			if _, err := st.db.Exec(`UPDATE task_sessions SET state = ?, archived_at = ?, conversation_id = ? WHERE id = ?`,
				tc.state, tc.archived, tc.conv, a); err != nil {
				t.Fatal(err)
			}
			if tc.other != "" {
				addSession(t, st, "p", "c", tc.other, "")
			}
			if err := h.Tick(); err != nil {
				t.Fatal(err)
			}
			if got := cmdOutcome(t, st, c.ID); got.Status != CmdFailed || got.Error != errMissionChanged {
				t.Fatalf("command = %s %q", got.Status, got.Error)
			}
			if len(l.started) != 0 || confirmRows(t, st) != 0 {
				t.Fatal("started, or record left")
			}
		})
	}
}

func TestTickRefusesAConversationMismatchWithTheSameSessionNumber(t *testing.T) {
	h, l, st := newHost(t)
	other := addSession(t, st, "p", "other", "running", "") // a live session, but of another conversation
	c := confirmedAt(t, st, "c", "t1", other)
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := cmdOutcome(t, st, c.ID); got.Status != CmdFailed || got.Error != errMissionChanged {
		t.Fatalf("command = %s %q", got.Status, got.Error)
	}
	if len(l.started) != 0 {
		t.Fatal("started")
	}
}
