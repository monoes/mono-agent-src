package tasks

import "testing"

// The chat a boss answers to is named by the boss session's client id; the lookup finds the boss only once the operator
// stamped it, so a session an agent gave the same client id for its own chat is never taken for one.
func TestOpenBossConversationNeedsTheStampAndTheFrontDeskKey(t *testing.T) {
	s, db, _ := newTestStore(t)
	front := "11111111-2222-3333-4444-555555555555"
	key := BossClientID(front)
	if len(key) > MaxClientIDLen {
		t.Fatalf("the key %q is longer than a client id may be", key)
	}
	boss, err := s.CreateSession(bg, "default", SessionInput{Title: "boss", Kind: SessionKindMission, ConversationID: "boss-conv", ClientID: key}, human)
	if err != nil {
		t.Fatal(err)
	}
	conv, id, err := OpenBossConversation(bg, db, "default", front)
	if err != nil || conv != "" || id != 0 {
		t.Fatalf("an unstamped session is a boss: %q %d %v", conv, id, err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET doomsday_charter_id = 'c1', doomsday_generation = 1 WHERE id = ?`, boss.ID); err != nil {
		t.Fatal(err)
	}
	conv, id, err = OpenBossConversation(bg, db, "default", front)
	if err != nil || conv != "boss-conv" || id != boss.ID {
		t.Fatalf("stamped boss not found: %q %d %v", conv, id, err)
	}
	if conv, id, _ = OpenBossConversation(bg, db, "default", "another-chat"); conv != "" || id != 0 {
		t.Fatalf("another chat got the boss: %q %d", conv, id)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET archived_at = '2026-11-06T00:00:00Z' WHERE id = ?`, boss.ID); err != nil {
		t.Fatal(err)
	}
	if conv, id, _ = OpenBossConversation(bg, db, "default", front); conv != "" || id != 0 {
		t.Fatalf("an archived boss answers: %q %d", conv, id)
	}
}
