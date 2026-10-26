package coderhost

import (
	"context"
	"testing"
	"time"
)

// A command the host read just before its conversation was deleted must not bring the session
// back or start a turn on nothing.
func TestACommandForADeletedConversationDoesNotBringTheSessionBack(t *testing.T) {
	h, l, st := newHost(t)
	h.RequireConversations = true
	addConversation(t, st, "c1", "/work/c1")
	c := send(t, st, "c1", "t1", "hello")
	st.EnsureSession("p", "c1")
	if _, err := st.db.Exec(`DELETE FROM ai_chat_conversations WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`DELETE FROM coder_sessions WHERE conversation_id = 'c1'`) // what the delete itself removes
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.GetCommand(c.ID); got.Status != CmdFailed || got.Error != "the conversation was deleted" {
		t.Fatalf("command = %+v", got)
	}
	if _, ok, _ := st.GetSession("p", "c1"); ok {
		t.Fatal("the host re-created the session of a deleted conversation")
	}
	if len(l.started) != 0 {
		t.Fatalf("started = %v", l.started)
	}
	// a stop or a pause does not bring it back either
	st.Enqueue(Command{ProfileID: "p", ConversationID: "c1", Kind: KindPause})
	_ = h.Tick()
	if _, ok, _ := st.GetSession("p", "c1"); ok {
		t.Fatal("a pause re-created the session of a deleted conversation")
	}
}

func TestSweepOrphansRemovesOnlyWhatBelongsToDeletedConversations(t *testing.T) {
	_, _, st := newHost(t)
	addConversation(t, st, "live", "/work/live")
	for _, id := range []string{"live", "ghost"} {
		if _, err := st.EnsureSession("p", id); err != nil {
			t.Fatal(err)
		}
		st.Enqueue(Command{ProfileID: "p", ConversationID: id, Kind: KindPause})
	}
	done, _ := st.Enqueue(Command{ProfileID: "p", ConversationID: "ghost", Kind: KindResume})
	st.SetCommand(done.ID, CmdDone, "") // the record of a command that ran is history, not a ghost
	n, err := st.SweepOrphans()
	if err != nil || n != 2 {
		t.Fatalf("swept %d, %v; want the ghost's session and its pending command", n, err)
	}
	if _, ok, _ := st.GetSession("p", "ghost"); ok {
		t.Fatal("the ghost's session survived")
	}
	if _, ok, _ := st.GetSession("p", "live"); !ok {
		t.Fatal("a live conversation's session was swept")
	}
	if st.PendingFor("p", "live") != 1 || st.PendingFor("p", "ghost") != 0 {
		t.Fatalf("pending: live %d ghost %d", st.PendingFor("p", "live"), st.PendingFor("p", "ghost"))
	}
	if again, _ := st.SweepOrphans(); again != 0 {
		t.Fatalf("a second sweep removed %d", again)
	}
}

// The running host sweeps by itself, so a row that slipped in is gone within a pass.
func TestRunSweepsSessionsOfDeletedConversations(t *testing.T) {
	h, _, st := newHost(t)
	h.RequireConversations = true
	h.Poll, h.CheckinEvery = 5*time.Millisecond, 5*time.Millisecond
	st.EnsureSession("p", "ghost")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	waitFor(t, "the ghost to be swept", func() bool { _, ok, _ := st.GetSession("p", "ghost"); return !ok })
	cancel()
	<-done
}
