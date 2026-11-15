package coderhost

import (
	"fmt"
	"testing"
)

// A session left in waiting_user because its release failed after a good answer is moved on by the
// host's next pass, once nothing waits on the person; one that still has an open question is left alone.
func TestTickRepairsASessionLeftWaitingAfterAFailedRelease(t *testing.T) {
	h, _, st := newHost(t)
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	lift := failWrites(t, st, "no_release", "UPDATE", "coder_sessions", "NEW.state = 'idle'")
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"}); err == nil {
		t.Fatal("the failed release was not reported")
	}
	lift()
	if _, err := st.db.Exec(`UPDATE coder_session_commands SET status = 'done'`); err != nil { // the note ran: the pass must not start a turn
		t.Fatal(err)
	}
	if got := sessionState(t, st, "k1"); got != StateWaitingUser {
		t.Fatalf("setup: session is %s", got)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := sessionState(t, st, "k1"); got != StateIdle {
		t.Fatalf("the next pass left the session %s", got)
	}
	// nothing to repair: a second pass changes nothing, not even updated_at
	before := state(t, st, "k1")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if after := state(t, st, "k1"); after != before {
		t.Fatalf("an idempotent pass rewrote the session: %+v -> %+v", before, after)
	}
}

func TestTickKeepsASessionWaitingWhileAQuestionIsOpen(t *testing.T) {
	h, _, st := newHost(t)
	childOf(t, st)
	if _, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := sessionState(t, st, "k1"); got != StateWaitingUser {
		t.Fatalf("a session with an open question was moved to %s", got)
	}
}

// A permanently failing give-back must not keep the rows behind it from being tried.
func TestReopenFailedAnswersDoesNotStarveLaterRows(t *testing.T) {
	st, _ := parkStore(t)
	q, cmd := masterAnswer(t, st)
	if _, err := st.CancelPendingSends("p", "k1", cmd.ID+1, "cancelled by a stop"); err != nil {
		t.Fatal(err)
	}
	clone := func() int64 {
		res, err := st.db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, question, options, default_answer, reversible, status, answer, answered_by, command_id, created_at, answered_at)
			SELECT profile_id, conversation_id, class, question, options, default_answer, reversible, status, answer, answered_by, command_id, created_at, answered_at FROM coder_session_questions WHERE id = ?`, q.ID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	lastBad := q.ID
	for i := 0; i < 249; i++ {
		lastBad = clone()
	}
	good := clone()
	failWrites(t, st, "no_reopen_old", "UPDATE", "coder_session_questions", fmt.Sprintf("NEW.status = 'open' AND NEW.id <= %d", lastBad))
	for pass := 0; pass < 3; pass++ {
		_ = st.ReopenFailedAnswers() // the failing rows are reported every pass
	}
	if got := questionOf(t, st, good); got.Status != "open" {
		t.Fatalf("the answerable row stayed %s behind %d failing ones", got.Status, lastBad)
	}
	if got := questionOf(t, st, q.ID); got.Status == "open" {
		t.Fatal("a failing row was re-opened")
	}
}
