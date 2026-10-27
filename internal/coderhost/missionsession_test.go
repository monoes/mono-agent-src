package coderhost

import "testing"

func addSession(t *testing.T, st *Store, profile, conv, state, archived string) int64 {
	t.Helper()
	if _, err := st.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES (?, ?)`, profile, profile); err != nil {
		t.Fatal(err)
	}
	res, err := st.db.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, state, archived_at, created_by, created_at, updated_at)
		VALUES (?, ?, 't', ?, ?, 'x', 'n', 'n')`, profile, conv, state, archived)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// confirmedAt queues a mission send and confirms it bound to the given session.
func confirmedAt(t *testing.T, st *Store, conv, turn string, session int64) Command {
	t.Helper()
	c, err := st.Enqueue(Command{ProfileID: "p", ConversationID: conv, Kind: KindSend, TurnID: turn, Message: "go", WantsMission: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Confirm(c, session); err != nil {
		t.Fatal(err)
	}
	return c
}

func cmdOutcome(t *testing.T, st *Store, id int64) Command {
	t.Helper()
	c, ok, err := st.GetCommand(id)
	if err != nil || !ok {
		t.Fatalf("command %d: %v %v", id, ok, err)
	}
	return c
}

func TestTickRefusesAConfirmationBoundToAnotherSession(t *testing.T) {
	h, l, st := newHost(t)
	a := addSession(t, st, "p", "c", "running", "")
	c := confirmedAt(t, st, "c", "t1", a)
	b := addSession(t, st, "p", "c", "draft", "") // the newest open one is now B
	if b <= a {
		t.Fatal("setup")
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	got := cmdOutcome(t, st, c.ID)
	if got.Status != CmdFailed || got.Error != errMissionChanged {
		t.Fatalf("command = %s %q, want failed with the changed-mission reason", got.Status, got.Error)
	}
	if len(l.started) != 0 {
		t.Fatalf("a turn started: %v", l.started)
	}
	if n := confirmRows(t, st); n != 0 {
		t.Fatalf("%d record(s) left after the refusal", n)
	}
}

func TestTickStartsWhenTheBoundSessionIsStillTheOpenOne(t *testing.T) {
	h, l, st := newHost(t)
	addSession(t, st, "p", "c", "done", "") // older closed ones do not matter
	a := addSession(t, st, "p", "c", "paused", "")
	c := confirmedAt(t, st, "c", "t1", a)
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := cmdOutcome(t, st, c.ID); got.Status != CmdDone {
		t.Fatalf("command = %s %q", got.Status, got.Error)
	}
	if tok, ok := tokenOf(l, "t1"); !ok || tok == "" {
		t.Fatalf("the child was not given the token (%q, %v)", tok, ok)
	}
}

func TestTickBoundZeroHoldsOnlyWhileThereIsNoOpenSession(t *testing.T) {
	h, l, st := newHost(t)
	// closed, archived and other-conversation or other-profile sessions are not open
	addSession(t, st, "p", "c", "done", "")
	addSession(t, st, "p", "c", "stopped", "")
	addSession(t, st, "p", "c", "failed", "")
	addSession(t, st, "p", "c", "running", "2026-10-26T00:00:00Z")
	addSession(t, st, "p", "other", "running", "")
	addSession(t, st, "q", "c", "running", "")
	ok := confirmedAt(t, st, "c", "t1", 0)
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := cmdOutcome(t, st, ok.ID); got.Status != CmdDone {
		t.Fatalf("bound 0, nothing open: %s %q", got.Status, got.Error)
	}
	if _, started := tokenOf(l, "t1"); !started {
		t.Fatal("not started")
	}
	// a session appeared since the confirmation: not adopted
	h2, l2, st2 := newHost(t)
	c := confirmedAt(t, st2, "c", "t2", 0)
	addSession(t, st2, "p", "c", "draft", "")
	if err := h2.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := cmdOutcome(t, st2, c.ID); got.Status != CmdFailed || got.Error != errMissionChanged {
		t.Fatalf("bound 0 with a session since: %s %q", got.Status, got.Error)
	}
	if len(l2.started) != 0 || confirmRows(t, st2) != 0 {
		t.Fatal("started, or record left")
	}
}

func TestTickIgnoresAnotherProfilesSessionIDs(t *testing.T) {
	h, l, st := newHost(t)
	other := addSession(t, st, "q", "c", "running", "")
	c := confirmedAt(t, st, "c", "t1", other) // bound to a session id of another profile
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := cmdOutcome(t, st, c.ID); got.Status != CmdFailed {
		t.Fatalf("a foreign session id matched: %s", got.Status)
	}
	if len(l.started) != 0 {
		t.Fatal("started")
	}
}
