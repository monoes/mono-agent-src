package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/aitask"
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
	// A bound token names a task of a live conversation of the profile.
	ts := &aitask.Store{DB: db, Profile: "p1"}
	for id, conv := range map[string]string{"at-1": gone.ID, "at-2": kept.ID} {
		if _, err := ts.Insert(aitask.Task{ID: id, Kind: "generic", ConversationID: conv, Status: aitask.StatusRunning, Goal: "g"}); err != nil {
			t.Fatal(err)
		}
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

// A bound token cannot be issued once its conversation is gone, or for a task that is not that
// conversation's: the insert holds only while both exist, in the same write.
func TestChatEvents_BoundTokenNeedsALiveConversationAndItsTask(t *testing.T) {
	s, db := newChatEventStore(t)
	live, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	ts := &aitask.Store{DB: db, Profile: "p1"}
	for id, conv := range map[string]string{"at-live": live.ID, "at-gone": gone.ID} {
		if _, err := ts.Insert(aitask.Task{ID: id, Kind: "generic", ConversationID: conv, Status: aitask.StatusRunning, Goal: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	at := agenttokens.NewStore(db)
	ctx := context.Background()
	issue := func(name, conv, task, profile string) error {
		_, _, err := at.CreateBound(ctx, profile, name, agenttokens.ScopeWrite, time.Hour, agenttokens.Binding{ConversationID: conv, TaskID: task})
		return err
	}
	if err := issue("ok", live.ID, "at-live", "p1"); err != nil {
		t.Fatalf("a token for a live conversation and its task: %v", err)
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ conv, task, profile string }{
		"deleted-conversation":      {gone.ID, "at-gone", "p1"},
		"task-of-another-one":       {live.ID, "at-gone", "p1"},
		"unknown-conversation":      {"no-such", "at-live", "p1"},
		"conversation-of-a-profile": {live.ID, "at-live", "p2"},
	} {
		if err := issue("n-"+name, c.conv, c.task, c.profile); !errors.Is(err, agenttokens.ErrBindingTarget) {
			t.Errorf("%s: %v, want ErrBindingTarget", name, err)
		}
	}
}
