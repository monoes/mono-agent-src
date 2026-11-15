package coderhost

import (
	"strings"
	"testing"
	"time"
)

// failWrites makes every statement matching the trigger's event abort, the way a
// locked database refuses a write, until the returned function lifts it.
func failWrites(t *testing.T, st *Store, name, event, table, when string) func() {
	t.Helper()
	sql := `CREATE TRIGGER ` + name + ` BEFORE ` + event + ` ON ` + table + ` WHEN ` + when + ` BEGIN SELECT RAISE(ABORT, 'database is locked'); END`
	if _, err := st.db.Exec(sql); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := st.db.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatal(err)
		}
	}
}

func sessionState(t *testing.T, st *Store, conv string) State {
	t.Helper()
	x, ok, err := st.GetSession("p", conv)
	if err != nil || !ok {
		t.Fatalf("session %s: %v %v", conv, ok, err)
	}
	return x.State
}

// A refused queue write leaves the answered question for the redelivery pass; a
// refused re-open of it must be reported, not swallowed.
func TestAnswerReportsAReopenThatCouldNotBeWritten(t *testing.T) {
	st, clk := parkStore(t)
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	failWrites(t, st, "no_enqueue", "INSERT", "coder_session_commands", "1")
	failWrites(t, st, "no_reopen", "UPDATE", "coder_session_questions", "NEW.status = 'open'")
	_, _, err = st.Answer("p", q.ID, "yes", masterScope{conv: "m"})
	if err == nil || !strings.Contains(err.Error(), "could not be opened again") {
		t.Fatalf("the failed re-open was not reported: %v", err)
	}
	if got := questionOf(t, st, q.ID); got.Status != "answered" || got.CommandID != 0 {
		t.Fatalf("question = %+v", got)
	}
	// nothing was lost: once the database accepts writes the redelivery pass queues the note
	if _, err := st.db.Exec(`DROP TRIGGER no_enqueue`); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(redeliverAfter + time.Second)
	if _, err := st.ApplyDefaults(); err != nil {
		t.Fatal(err)
	}
	if len(answerCommands(t, st, "k1")) != 1 {
		t.Fatal("the answer was never delivered")
	}
}

// A delivered answer whose session could not be moved out of waiting_user is reported.
func TestAnswerReportsASessionThatCouldNotBeReleased(t *testing.T) {
	st, _ := parkStore(t)
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	failWrites(t, st, "no_release", "UPDATE", "coder_sessions", "NEW.state = 'idle'")
	_, cmd, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"})
	if err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("the unreleased session was not reported: %v", err)
	}
	if cmd.ID == 0 {
		t.Fatal("the queued command is not reported with the error")
	}
}

// The re-open of a failed answer is one unit: the question, its decision and the
// session move together, or none of them does and the next pass tries again.
func TestReopenFailedAnswersIsAllOrNothing(t *testing.T) {
	st, _ := parkStore(t)
	q, cmd := masterAnswer(t, st)
	if _, err := st.CancelPendingSends("p", "k1", cmd.ID+1, "cancelled by a stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET state = 'idle' WHERE conversation_id = 'k1'`); err != nil {
		t.Fatal(err)
	}
	lift := failWrites(t, st, "no_wait", "UPDATE", "coder_sessions", "NEW.state = 'waiting_user'")
	if err := st.ReopenFailedAnswers(); err == nil {
		t.Fatal("the failed give-back was not reported")
	}
	if got := questionOf(t, st, q.ID); got.Status == "open" {
		t.Fatalf("the question was re-opened while its session was left %s", sessionState(t, st, "k1"))
	}
	lift()
	if err := st.ReopenFailedAnswers(); err != nil {
		t.Fatal(err)
	}
	if got := questionOf(t, st, q.ID); got.Status != "open" || sessionState(t, st, "k1") != StateWaitingUser {
		t.Fatalf("the next pass did not finish it: %+v, %s", got, sessionState(t, st, "k1"))
	}
}

// A question of a conversation that is gone is removed with its person item, or not at all.
func TestApplyDefaultsDropsAGoneQuestionWithItsItemOrNotAtAll(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q, err := st.Park("p", "a", "risky_action", "force push?", nil, "d", false)
	if err != nil {
		t.Fatal(err)
	}
	if !st.hasPersonItem("p", q.ID) {
		t.Fatal("no person item to lose")
	}
	if _, err := st.db.Exec(`DELETE FROM ai_chat_conversations WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(ParkTimeout + time.Second)
	lift := failWrites(t, st, "no_item_drop", "DELETE", "coder_session_person_items", "1")
	_, err = st.ApplyDefaults()
	_, qerr := st.GetQuestion("p", q.ID)
	if qerr == nil != st.hasPersonItem("p", q.ID) {
		t.Fatalf("question present=%v, item present=%v: half removed", qerr == nil, st.hasPersonItem("p", q.ID))
	}
	if err == nil {
		t.Fatal("the failed removal was not reported")
	}
	lift()
	if _, err := st.ApplyDefaults(); err != nil {
		t.Fatal(err)
	}
	if _, qerr := st.GetQuestion("p", q.ID); qerr == nil || st.hasPersonItem("p", q.ID) {
		t.Fatal("the next pass did not remove it")
	}
}
