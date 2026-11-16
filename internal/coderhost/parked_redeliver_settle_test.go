package coderhost

import "testing"

// A redelivery that keeps failing with nothing queued must not leave a live session labelled waiting_user
// with no open question: the claim-window guard of RepairWaiting never expires while the redelivery retries
// (each attempt moves answered_at forward), so the failed attempt itself settles the label.
func TestFailedRedeliverySettlesAWaitingSessionWithNoOpenQuestion(t *testing.T) {
	st, clk := parkStore(t)
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.claimQuestion(q, questionStatusAnswer, "yes", WhoMaster+":m"); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * redeliverAfter)
	failWrites(t, st, "no_queue", "INSERT", "coder_session_commands", "1")
	if err := st.redeliver(); err == nil {
		t.Fatal("the redelivery was expected to fail")
	}
	if got := sessionState(t, st, "k1"); got != StateIdle {
		t.Fatalf("session = %s after a failed redelivery with no open question", got)
	}
	if got := questionOf(t, st, q.ID); got.CommandID != 0 || got.Status == "open" {
		t.Fatalf("the answer was not kept for the next redelivery: %+v", got)
	}
}

// ...but a failed redelivery never idles a session that still has an open question.
func TestFailedRedeliveryKeepsASessionWaitingWhileAQuestionIsOpen(t *testing.T) {
	st, clk := parkStore(t)
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Park("p", "k1", "risky_action", "second?", nil, "", false); err != nil {
		t.Fatal(err)
	}
	if err := st.claimQuestion(q, questionStatusAnswer, "yes", WhoMaster+":m"); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * redeliverAfter)
	failWrites(t, st, "no_queue", "INSERT", "coder_session_commands", "1")
	_ = st.redeliver()
	if got := sessionState(t, st, "k1"); got != StateWaitingUser {
		t.Fatalf("session = %s with a question still open", got)
	}
}
