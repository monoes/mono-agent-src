package ai

import (
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// A conversation's coder session goes with it: its row, the commands still waiting for it, and its
// children's link (they stay, as sessions of their own, so the master can go solo).
func TestChatEvents_DeleteConversationClearsItsCoderSession(t *testing.T) {
	s, db := newChatEventStore(t)
	master, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	child, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	other, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	otherProfile, _ := s.CreateConversation("p2", "agent", "general", "claude", "", "")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ profile, id, role, parent string }{
		{"p1", master.ID, "master", ""}, {"p1", child.ID, "child", master.ID}, {"p1", other.ID, "solo", ""}, {"p2", otherProfile.ID, "solo", ""},
	} {
		exec(`INSERT INTO coder_sessions (profile_id, conversation_id, role, parent_conversation_id, created_at, updated_at) VALUES (?, ?, ?, ?, 'now', 'now')`, c.profile, c.id, c.role, c.parent)
		exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, created_at, updated_at) VALUES (?, ?, 'send', 't', 'm', 'now', 'now')`, c.profile, c.id)
	}
	count := func(q string, args ...any) (n int) {
		t.Helper()
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if err := s.DeleteConversation(master.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM coder_sessions WHERE conversation_id = ?`, master.ID); n != 0 {
		t.Errorf("the deleted conversation's session row survived: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ?`, master.ID); n != 0 {
		t.Errorf("the commands waiting for the deleted conversation survived: %d", n)
	}
	var role, parent string
	if err := db.QueryRow(`SELECT role, parent_conversation_id FROM coder_sessions WHERE conversation_id = ?`, child.ID).Scan(&role, &parent); err != nil {
		t.Fatal(err)
	}
	if role != "solo" || parent != "" {
		t.Errorf("a child of the deleted master is %q with parent %q, want a solo session with no parent", role, parent)
	}
	for _, id := range []string{child.ID, other.ID, otherProfile.ID} {
		if count(`SELECT COUNT(*) FROM coder_sessions WHERE conversation_id = ?`, id) != 1 || count(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ?`, id) != 1 {
			t.Errorf("a session of another conversation or profile was touched: %s", id)
		}
	}
	// deleting the child leaves the other two sessions
	if err := s.DeleteConversation(child.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT COUNT(*) FROM coder_sessions`); n != 2 {
		t.Errorf("%d session rows left, want the 2 that were not deleted", n)
	}
}

// A conversation with a live host turn cannot be deleted, and nothing of its session is touched.
func TestChatEvents_DeleteConversationWithALiveHostTurnIsRefusedAndKeepsItsSession(t *testing.T) {
	s, db := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	if _, _, err := s.CreateTurn(conv.ID, "p1", "turn-1", "host-1a2b3c4d", "hello"); err != nil { // the host's turn is an active turn owned by the host
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, state, turn_id, turn_pid, created_at, updated_at) VALUES ('p1', ?, 'running', 'turn-1', 4242, 'now', 'now')`, conv.ID)
	db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, created_at, updated_at) VALUES ('p1', ?, 'send', 't2', 'waits', 'now', 'now')`, conv.ID)
	if err := s.DeleteConversation(conv.ID, "p1"); err != ErrTurnActive {
		t.Fatalf("err = %v, want ErrTurnActive", err)
	}
	var sessions, commands int
	db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE conversation_id = ?`, conv.ID).Scan(&sessions)
	db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE conversation_id = ?`, conv.ID).Scan(&commands)
	if sessions != 1 || commands != 1 {
		t.Fatalf("a refused delete touched the session: %d rows, %d commands", sessions, commands)
	}
	// once the turn has ended the delete goes through
	if _, _, err := s.FinalizeTurn("p1", conv.ID, "turn-1", chatevents.StatusCompleted, "", nil, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversation(conv.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE conversation_id = ?`, conv.ID).Scan(&sessions)
	if sessions != 0 {
		t.Fatal("the session row survived the delete")
	}
}
