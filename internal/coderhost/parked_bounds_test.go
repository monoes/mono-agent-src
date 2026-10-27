package coderhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func countAnswerNotes(t *testing.T, st *Store, prefix string) int {
	t.Helper()
	return countRows(t, st, `SELECT COUNT(*) FROM coder_session_commands WHERE substr(turn_id, 1, ?) = ?`, len(prefix), prefix)
}

// failAnswerNotes marks every pending answer note as failed, the way a run-time refusal would.
func failAnswerNotes(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.db.Exec(`UPDATE coder_session_commands SET status = 'failed', error = 'start refused' WHERE status = 'pending' AND substr(turn_id, 1, 7) = 'answer-'`); err != nil {
		t.Fatal(err)
	}
	st.ReopenFailedAnswers()
}

func TestADefaultWhoseNoteKeepsFailingIsTriedAtMostTwice(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q := defaultable(t, st, clk, "a")
	// many passes at the same moment: the first attempt fails, and a failure within ParkTimeout holds the next
	for i := 0; i < 5; i++ {
		st.ApplyDefaults() //nolint:errcheck
		failAnswerNotes(t, st)
	}
	if n := countAnswerNotes(t, st, "answer-d-"); n != 1 {
		t.Fatalf("%d default notes within one ParkTimeout, want 1", n)
	}
	// later passes: one more try, then it is left for a person
	for i := 0; i < 6; i++ {
		clk.t = clk.t.Add(ParkTimeout + time.Second)
		st.ApplyDefaults() //nolint:errcheck
		failAnswerNotes(t, st)
	}
	if n := countAnswerNotes(t, st, "answer-d-"); n != MaxAnswerReopens {
		t.Fatalf("%d default notes in all, want %d", n, MaxAnswerReopens)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM coder_session_decisions WHERE status = 'not_applied'`); n != MaxAnswerReopens {
		t.Fatalf("%d not_applied ledger rows", n)
	}
	if got := questionOf(t, st, q.ID); got.Status != "open" {
		t.Fatalf("status = %s: it should wait for a person", got.Status)
	}
}

func TestNoDefaultIsTakenWhenTheBudgetIsUsedUp(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q := defaultable(t, st, clk, "a")
	if _, err := st.db.Exec(`UPDATE coder_sessions SET budget_usd = 0.0000001 WHERE conversation_id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.TurnCap("p", "a"); err == nil {
		t.Fatal("the test budget is not used up")
	}
	for i := 0; i < 4; i++ {
		clk.t = clk.t.Add(ParkTimeout)
		if n, _ := st.ApplyDefaults(); n != 0 {
			t.Fatal("a default was applied with no budget to run it")
		}
	}
	if countAnswerNotes(t, st, "answer-") != 0 || questionOf(t, st, q.ID).Status != "open" {
		t.Fatal("a note or a claim was made")
	}
}

func TestAMastersAnswerThatKeepsFailingIsRefused(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	q, _ := st.Park("p", "k1", "risky_action", "go?", nil, "", false)
	for i := 0; i < MaxAnswerReopens; i++ {
		if _, _, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"}); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		failAnswerNotes(t, st)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"}); !errors.Is(err, ErrAnswerKeepsFailing) {
		t.Fatalf("third attempt = %v", err)
	}
	if n := countAnswerNotes(t, st, "answer-m-"); n != MaxAnswerReopens {
		t.Fatalf("%d master notes", n)
	}
}

func TestACommandThatFailedAfterItsTurnStartedDoesNotReopenTheQuestion(t *testing.T) {
	h, l, st := newHost(t)
	l.next = 0 // the first turn gets pid 1, which the session cannot record: the failure comes after Start
	addConversation(t, st, "a", "/w/a")
	q, _ := st.Park("p", "a", "risky_action", "go?", nil, "", false)
	_, cmd, err := st.Answer("p", q.ID, "yes", OperatorScope{})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	got, _, _ := st.GetCommand(cmd.ID)
	if got.Status != CmdFailed || !strings.HasPrefix(got.Error, startedMarker) || len(l.started) != 1 {
		t.Fatalf("command = %+v, started %v", got, l.started)
	}
	_ = h.Tick()
	if q := questionOf(t, st, q.ID); q.Status != "answered" {
		t.Fatalf("the model may have read the note, yet the question is %s", q.Status)
	}
}

func TestOnlyAFailedNoteReopensItsQuestion(t *testing.T) {
	st, _ := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q, _ := st.Park("p", "a", "risky_action", "go?", nil, "", false)
	_, cmd, _ := st.Answer("p", q.ID, "yes", OperatorScope{})
	st.ReopenFailedAnswers() // pending
	if questionOf(t, st, q.ID).Status != "answered" {
		t.Fatal("a pending note reopened its question")
	}
	st.SetCommand(cmd.ID, CmdDone, "") //nolint:errcheck // a turn started from it
	st.ReopenFailedAnswers()
	if questionOf(t, st, q.ID).Status != "answered" {
		t.Fatal("a note that ran reopened its question")
	}
}

func TestMasterAnswerCountsNotesAlreadyWaiting(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	q, _ := st.Park("p", "k1", "risky_action", "go?", nil, "", false)
	setSettings(t, st, "k1", `{"checkin_limit": 1}`)
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: "k1", Kind: KindSend, TurnID: answerTurnID(answerFromDefault), Message: "waiting"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"}); !errors.Is(err, ErrWaitsForPerson) {
		t.Fatalf("answer with a gated note already waiting = %v", err)
	}
}
