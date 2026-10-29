package ai

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// The guard runs inside the delete's transaction: a refusal deletes nothing, an error aborts, a yes deletes.
func TestChatEvents_DeleteConversationIfUnownedGuard(t *testing.T) {
	s, _ := newChatEventStore(t)
	c, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	verdict := func(ok bool, err error) func(context.Context, *sql.Conn) (bool, error) {
		return func(context.Context, *sql.Conn) (bool, error) { return ok, err }
	}
	if err := s.DeleteConversationIfUnowned(c.ID, "p1", verdict(false, nil)); !errors.Is(err, ErrConversationGuarded) {
		t.Fatalf("refusal: %v", err)
	}
	boom := errors.New("boom")
	if err := s.DeleteConversationIfUnowned(c.ID, "p1", verdict(true, boom)); !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
	if _, err := s.GetConversation(c.ID, "p1"); err != nil {
		t.Fatalf("the conversation must survive a refusal: %v", err)
	}
	if err := s.DeleteConversationIfUnowned(c.ID, "p1", verdict(true, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetConversation(c.ID, "p1"); err == nil {
		t.Fatal("deleted conversation still there")
	}
}
