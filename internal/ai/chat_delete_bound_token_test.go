package ai

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

// An agent token bound to a task and its conversation ends with the conversation: it is revoked in
// the same transaction as the delete. Another conversation's token, and an unbound one, are untouched.
func TestChatEvents_DeleteConversationRevokesItsBoundAgentTokens(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	at := agenttokens.NewStore(db)
	ctx := context.Background()
	mint := func(name, conv, task string) string {
		t.Helper()
		_, secret, err := at.CreateBound(ctx, "p1", name, agenttokens.ScopeWrite, time.Hour, agenttokens.Binding{ConversationID: conv, TaskID: task})
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	goneTok := mint("bound-gone", gone.ID, "at-1")
	keptTok := mint("bound-kept", kept.ID, "at-2")
	_, unbound, err := at.Create(ctx, "p1", "unbound", agenttokens.ScopeRead, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{goneTok, keptTok, unbound} {
		if _, err := at.Verify(ctx, tok); err != nil {
			t.Fatalf("a fresh token does not verify: %v", err)
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := at.Verify(ctx, goneTok); err == nil {
		t.Error("a token bound to the deleted conversation still verifies")
	}
	if _, err := at.Verify(ctx, keptTok); err != nil {
		t.Errorf("another conversation's bound token was revoked: %v", err)
	}
	if _, err := at.Verify(ctx, unbound); err != nil {
		t.Errorf("an unbound token was revoked: %v", err)
	}
}
