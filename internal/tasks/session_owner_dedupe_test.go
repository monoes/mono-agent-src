package tasks

import (
	"errors"
	"strings"
	"testing"
)

// A client id another chat used is never answered with that chat's session.
func TestAgentClientIDDedupeNeverReturnsAnotherChatsSession(t *testing.T) {
	s, db, _ := newTestStore(t)
	for _, id := range []string{"conv-a", "conv-b"} {
		if _, err := db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, 'default', 'x', '', '')`, id); err != nil {
			t.Fatal(err)
		}
	}
	agentB := Actor{Kind: Agent, Name: "b", Conversation: "conv-b"}
	b, err := s.CreateSession(bg, "default", SessionInput{Title: "b", ConversationID: "conv-b", ClientID: "K"}, agentB)
	if err != nil {
		t.Fatal(err)
	}
	unlinked, err := s.CreateSession(bg, "default", SessionInput{Title: "free", ClientID: "U"}, human)
	if err != nil {
		t.Fatal(err)
	}
	agentA := Actor{Kind: Agent, Name: "a", Conversation: "conv-a"}
	cases := []struct {
		name string
		in   SessionInput
		who  Actor
	}{
		{"a key of another chat", SessionInput{Title: "a", ConversationID: "conv-a", ClientID: "K"}, agentA},
		{"a key of another chat, no link asked", SessionInput{Title: "a", ClientID: "K"}, Actor{Kind: Agent, Name: "a"}},
		{"a key of an unlinked session, a link asked", SessionInput{Title: "a", ConversationID: "conv-a", ClientID: "U"}, agentA},
	}
	for _, c := range cases {
		got, err := s.CreateSession(bg, "default", c.in, c.who)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "already used") {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.ID != 0 || got.ConversationID != "" || got.Deduped {
			t.Fatalf("%s: a session came back: %+v", c.name, got)
		}
	}
	// the owner of the key still dedupes, and the operator is unchanged
	again, err := s.CreateSession(bg, "default", SessionInput{Title: "b", ConversationID: "conv-b", ClientID: "K"}, agentB)
	if err != nil || !again.Deduped || again.ID != b.ID {
		t.Fatalf("own key: %v %+v", err, again)
	}
	op, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ClientID: "K"}, human)
	if err != nil || !op.Deduped || op.ID != b.ID {
		t.Fatalf("operator: %v %+v", err, op)
	}
	if again, err := s.CreateSession(bg, "default", SessionInput{Title: "free", ClientID: "U"}, Actor{Kind: Agent, Name: "z"}); err != nil || !again.Deduped || again.ID != unlinked.ID {
		t.Fatalf("an unlinked key without a link: %v %+v", err, again)
	}
}
