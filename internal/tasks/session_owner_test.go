package tasks

import (
	"errors"
	"testing"
)

// An agent links a session only to its own chat; the operator links any chat of the profile.
func TestAgentLinksSessionOnlyToItsOwnChat(t *testing.T) {
	s, db, _ := newTestStore(t)
	other := addProfile(t, db, "other")
	for id, prof := range map[string]string{"conv-a": "default", "conv-b": "default", "conv-x": other} {
		if _, err := db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, ?, 'x', '', '')`, id, prof); err != nil {
			t.Fatal(err)
		}
	}
	rows := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM task_sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	refused := []struct {
		name  string
		actor Actor
		conv  string
	}{
		{"another chat of the profile", Actor{Kind: Agent, Name: "a", Conversation: "conv-a"}, "conv-b"},
		{"a chat of another profile", Actor{Kind: Agent, Name: "a", Conversation: "conv-x"}, "conv-x"},
		{"no verified chat", Actor{Kind: Agent, Name: "a"}, "conv-a"},
	}
	for _, c := range refused {
		if _, err := s.CreateSession(bg, "default", SessionInput{Title: "t", ConversationID: c.conv}, c.actor); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n := rows(); n != 0 {
			t.Fatalf("%s: a refused call left %d sessions", c.name, n)
		}
	}
	own := Actor{Kind: Agent, Name: "a", Conversation: "conv-a"}
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "own", ConversationID: "conv-a"}, own)
	if err != nil || se.ConversationID != "conv-a" {
		t.Fatalf("own chat: %v %+v", err, se)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "free"}, Actor{Kind: Agent, Name: "a"}); err != nil {
		t.Fatalf("no link: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "op", ConversationID: "conv-b"}, human); err != nil {
		t.Fatalf("operator: %v", err)
	}
	if n := rows(); n != 3 {
		t.Fatalf("%d sessions, want 3", n)
	}
}
