package ai

import "testing"

// The spawn advisory's remembered refusal belongs to the lead that was refused: it goes with that lead's chat and
// leaves another lead's alone.
func TestChatEvents_DeleteConversationRemovesItsSpawnAdvice(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, c := range []string{gone.ID, kept.ID} {
		if _, err := db.Exec(`INSERT INTO coder_spawn_advice (profile_id, spawner, scope_hash, created_at) VALUES ('p1', ?, 'h', 'now')`, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_spawn_advice WHERE spawner = ?`, gone.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d advice row(s) of the deleted lead survived", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_spawn_advice WHERE spawner = ?`, kept.ID).Scan(&n)
	if n != 1 {
		t.Errorf("the other lead's advice: %d", n)
	}
}
