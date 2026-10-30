package tasks

import (
	"errors"
	"testing"
)

// Review finding 3: an agent renames only a draft it created from its own chat; naming oneself like the
// creator (--as) is not enough, and the operator's approval closes the title to agents.
func TestAgentRenamesOnlyItsOwnDraft(t *testing.T) {
	s, db, _ := newTestStore(t)
	chatRow(t, s, "c1")
	chatRow(t, s, "c2")
	a := Actor{Kind: Agent, Name: "a", Conversation: "c1"}
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "mine", ConversationID: "c1"}, a)
	if err != nil {
		t.Fatal(err)
	}
	for name, who := range map[string]Actor{
		"another chat, same name": {Kind: Agent, Name: "a", Conversation: "c2"},
		"no chat, same name":      {Kind: Agent, Name: "a"},
	} {
		if _, err := s.RenameSession(bg, "default", se.ID, "hijacked", who); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got, err := s.RenameSession(bg, "default", se.ID, "better", a); err != nil || got.Title != "better" {
		t.Fatalf("the creator renaming its draft: %v %+v", err, got)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameSession(bg, "default", se.ID, "after approval", a); err == nil {
		t.Fatal("an agent renamed a session the operator had approved")
	}
	if got, err := s.RenameSession(bg, "default", se.ID, "operator's", human); err != nil || got.Title != "operator's" {
		t.Fatalf("the operator: %v %+v", err, got)
	}
}

// Review 47: the creator's name is a label, not a credential. A draft started with no chat recorded cannot
// be told from one an agent merely names itself after, so an agent (even one passing the creator's name,
// bound to nothing and with no chat) never renames it; the operator does.
func TestAgentCannotRenameAChatlessDraftByItsCreatorsName(t *testing.T) {
	s, _, _ := newTestStore(t)
	chatRow(t, s, "c1")
	creator := Actor{Kind: Agent, Name: "a"}
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "chatless"}, creator)
	if err != nil {
		t.Fatal(err)
	}
	for name, who := range map[string]Actor{
		"the creator's name, no chat": creator,
		"the creator's name, a chat":  {Kind: Agent, Name: "a", Conversation: "c1"},
	} {
		if _, err := s.RenameSession(bg, "default", se.ID, "hijacked", who); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got, err := s.GetSession(bg, "default", se.ID, human); err != nil || got.Title != "chatless" {
		t.Fatalf("a refused rename changed the title: %v %+v", err, got)
	}
	if got, err := s.RenameSession(bg, "default", se.ID, "operator's", human); err != nil || got.Title != "operator's" {
		t.Fatalf("the operator: %v %+v", err, got)
	}
}
