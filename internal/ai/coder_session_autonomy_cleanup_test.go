package ai

import "testing"

// The decisions ledger and parked questions of a conversation go with it.
func TestChatEvents_DeleteConversationClearsItsDecisionsAndQuestions(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, id := range []string{gone.ID, kept.ID} {
		if _, err := db.Exec(`INSERT INTO coder_session_decisions (profile_id, conversation_id, class, made_by, created_at) VALUES ('p1', ?, 'ambiguity', 'model', 'now')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, created_at) VALUES ('p1', ?, 'risky_action', 'now')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	count := func(table, id string) (n int) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE conversation_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{"coder_session_decisions", "coder_session_questions"} {
		if n := count(table, gone.ID); n != 0 {
			t.Errorf("%s: %d row(s) of the deleted conversation survived", table, n)
		}
		if n := count(table, kept.ID); n != 1 {
			t.Errorf("%s: the other conversation has %d row(s)", table, n)
		}
	}
}
