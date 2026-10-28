package ai

import (
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
)

// Deleting a conversation revokes the token of its AI task and closes the task:
// the token carries the conversation's privileges, and nothing is left to
// replace it (lowering the access does the same). Another conversation's task is untouched.
func TestChatEvents_DeleteConversationRevokesItsTaskToken(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	ts := &aitask.Store{DB: db, Profile: "p1"}
	tokens := map[string]string{}
	ids := map[string]string{}
	for _, c := range []string{gone.ID, kept.ID} {
		task, err := ts.Insert(aitask.Task{ID: aitask.NewID(), Kind: "generic", ConversationID: c, Status: aitask.StatusNeedsUser, Goal: "g"})
		if err != nil {
			t.Fatal(err)
		}
		tokens[c], ids[c] = aitask.NewToken(), task.ID
		if err := ts.SetToken(task.ID, tokens[c]); err != nil {
			t.Fatal(err)
		}
		if !ts.CheckToken(task.ID, tokens[c]) {
			t.Fatal("a fresh token does not work")
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	if ts.CheckToken(ids[gone.ID], tokens[gone.ID]) {
		t.Error("the deleted conversation's task token still works")
	}
	if got, err := ts.Get(ids[gone.ID]); err != nil || got.Status != aitask.StatusCancelled {
		t.Errorf("the deleted conversation's task = %+v, %v; want cancelled", got, err)
	}
	if !ts.CheckToken(ids[kept.ID], tokens[kept.ID]) {
		t.Error("another conversation's task token was revoked")
	}
	if got, _ := ts.Get(ids[kept.ID]); got.Status != aitask.StatusNeedsUser {
		t.Errorf("another conversation's task is %q", got.Status)
	}
}
