package mcp

import (
	"strings"
	"testing"
)

func TestSessionAnswerAnswersOnlyAsTheChildsMaster(t *testing.T) {
	sf := newSessionFixture(t)
	qa, err := sf.store.Park(sf.profile, sf.a, "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	qb, _ := sf.store.Park(sf.profile, sf.b, "risky_action", "other?", nil, "", false)

	// the child answering its own question, a peer, and a forged token are refused with the one text
	for _, c := range []struct{ tok, session string }{{sf.tokA, sf.a}, {sf.tokA, sf.b}, {sf.tokA, sf.master}} {
		_, err := sf.as(t, c.tok, "session_answer", map[string]any{"session": c.session, "question_id": qa.ID, "answer": "yes"})
		if err == nil || !strings.Contains(err.Error(), "not_yours") {
			t.Errorf("child answering for %s: %v", c.session, err)
		}
	}
	if _, err := sf.as(t, "mst-forged", "session_answer", map[string]any{"session": sf.a, "question_id": qa.ID, "answer": "yes"}); err == nil || !strings.Contains(err.Error(), "no_session") {
		t.Errorf("forged: %v", err)
	}
	// the master with another child's question id under the wrong session, and an unknown id
	for _, id := range []int64{qb.ID, 9999} {
		_, e1 := sf.as(t, sf.tokM, "session_answer", map[string]any{"session": sf.a, "question_id": id, "answer": "yes"})
		if e1 == nil || !strings.Contains(e1.Error(), "not_yours") {
			t.Errorf("question %d under the wrong session: %v", id, e1)
		}
	}
	if sf.store.OpenParked(sf.profile, sf.a) != 1 {
		t.Fatal("a refused answer closed the question")
	}
	out, err := sf.as(t, sf.tokM, "session_answer", map[string]any{"session": sf.a, "question_id": qa.ID, "answer": "no, ask first"})
	if err != nil || !strings.Contains(out, "commandId") {
		t.Fatalf("master answer = %v %s", err, out)
	}
	q, _ := sf.store.GetQuestion(sf.profile, qa.ID)
	if q.AnsweredBy != "master:"+sf.master {
		t.Fatalf("answered by %q", q.AnsweredBy)
	}
	var n int
	sf.f.Side.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE kind = 'send' AND turn_id LIKE 'answer-m-%' AND issuer = ?`, sf.master).Scan(&n)
	if n != 1 {
		t.Fatalf("answer-m- commands = %d", n)
	}
	if _, err := sf.as(t, sf.tokM, "session_answer", map[string]any{"session": sf.a, "question_id": qa.ID, "answer": "again"}); err == nil || !strings.Contains(err.Error(), "invalid_input") {
		t.Fatalf("second answer: %v", err)
	}
}
