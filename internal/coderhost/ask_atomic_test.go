package coderhost

import (
	"errors"
	"testing"
)

func (f *askTree) openAsks(conv string) (n int) {
	_ = f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_asks WHERE asker_conv = ? AND status = 'open'`, conv).Scan(&n)
	return n
}

// A crash between the ask row and its person item (critical) or its lead command (normal) must not leave an open
// ask that takes a slot, is in nobody's inbox and never expires: the ask and its delivery are one commit.
func TestAskRowAndItsDeliveryCommitTogether(t *testing.T) {
	for _, imp := range []string{AskCritical, AskNormal} {
		f := newAskTree(t)
		f.st.afterAskInsert = func() error { return errors.New("crash") }
		if _, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: imp}); err == nil {
			t.Fatalf("%s: the crash was not seen", imp)
		}
		if n := f.openAsks(f.cc); n != 0 {
			t.Fatalf("%s: a crashed ask left %d open ask(s) with no delivery", imp, n)
		}
		f.st.afterAskInsert = nil
		f.tick()
		got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: imp})
		if err != nil {
			t.Fatalf("%s: the retry: %v", imp, err)
		}
		items, _ := f.st.OpenPersonItems("p")
		switch imp {
		case AskCritical:
			if len(items) != 1 || items[0].RefID != got.AskID {
				t.Fatalf("critical: the person's inbox: %+v", items)
			}
		default:
			if len(f.commandsTo(f.rc)) != 1 {
				t.Fatalf("normal: commands to the lead: %+v", f.commandsTo(f.rc))
			}
		}
	}
}

// A crash between the answer and the reply command leaves the ask open and retryable, with no reply lost.
func TestAnswerAndItsReplyCommitTogether(t *testing.T) {
	f := newAskTree(t)
	n, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.st.AskAncestor(f.lead(f.gc), "p", AskRequest{Question: "q", Level: "root", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	f.st.afterAnswerBeforeReply = func() error { return errors.New("crash") }
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", n.AskID, "r"); err == nil {
		t.Fatal("the crash was not seen")
	}
	if _, _, err := f.st.AnswerAncestorQuestion("p", c.AskID, "a"); err == nil {
		t.Fatal("the crash was not seen")
	}
	for _, id := range []int64{n.AskID, c.AskID} {
		if a, _ := f.st.GetAsk("p", id); a.Status != askOpen {
			t.Fatalf("ask %d is %s after a crash that delivered nothing", id, a.Status)
		}
	}
	if len(f.commandsTo(f.cc))+len(f.commandsTo(f.gc)) != 0 {
		t.Fatal("a reply was queued")
	}
	f.st.afterAnswerBeforeReply = nil
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", n.AskID, "r"); err != nil {
		t.Fatalf("the lead's retry: %v", err)
	}
	if _, _, err := f.st.AnswerAncestorQuestion("p", c.AskID, "a"); err != nil {
		t.Fatalf("the person's retry: %v", err)
	}
	if len(f.commandsTo(f.cc)) != 1 || len(f.commandsTo(f.gc)) != 1 {
		t.Fatalf("replies: %d, %d", len(f.commandsTo(f.cc)), len(f.commandsTo(f.gc)))
	}
}

// A normal ask older than AskTTL is expired when it is read or answered, with no other ask to trigger it.
func TestAnAskOlderThanTheTTLCannotBeAnswered(t *testing.T) {
	f := newAskTree(t)
	a, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "old"})
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(AskTTL + AskInterval)
	if got, _ := f.st.GetAsk("p", a.AskID); got.Status != askExpired {
		t.Fatalf("read: %s", got.Status)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", a.AskID, "late"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("an expired ask took a reply: %v", err)
	}
	if n := len(f.commandsTo(f.cc)); n != 0 {
		t.Fatalf("a late reply was queued: %d", n)
	}
}

// The tree is read again where the reply commits: a lead that stops being above the asker between the check and the
// commit cannot answer.
func TestAReplyIsRefusedWhenTheAskerMovesBetweenTheCheckAndTheCommit(t *testing.T) {
	f := newAskTree(t)
	a, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	f.st.beforeAskReplyTx = func() {
		if _, err := f.st.db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, f.R.ID, f.C.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", a.AskID, "r"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("the old addressee replied: %v", err)
	}
	if n := len(f.commandsTo(f.cc)); n != 0 {
		t.Fatalf("a reply reached the asker: %d", n)
	}
	if got, _ := f.st.GetAsk("p", a.AskID); got.Status == askAnswered {
		t.Fatal("the ask was marked answered")
	}
}

// Replies the host has not yet delivered cannot fill the chat's queue: the person's send still fits.
func TestAskNotesCannotExhaustThePersonsQueue(t *testing.T) {
	f := newAskTree(t)
	for i := 0; i < MaxPendingPerChat; i++ {
		a, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
		if err != nil {
			break // the channel may refuse once its notes wait: that is the cap
		}
		if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", a.AskID, "r"); err != nil {
			break
		}
		f.tick()
	}
	var notes int
	_ = f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE status = 'pending' AND substr(turn_id, 1, 6) IN ('ask-q-', 'ask-r-')`).Scan(&notes)
	if notes > 2*MaxPendingAskNotes || notes == 0 {
		t.Fatalf("%d ask notes wait", notes)
	}
	if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: f.rc, Kind: KindSend, TurnID: "person-turn", Message: "person"}); err != nil {
		t.Fatalf("the person cannot send to the lead: %v", err)
	}
	if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: f.cc, Kind: KindSend, TurnID: "person-turn", Message: "person"}); err != nil {
		t.Fatalf("the person cannot send to the asker: %v", err)
	}
}

// The last AskPersonReserve send slots of a chat are the person's: an ask cannot take them.
func TestAnAskCannotTakeThePersonsReservedSlots(t *testing.T) {
	f := newAskTree(t)
	for i := 0; i < MaxPendingPerChat-AskPersonReserve; i++ {
		if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: f.rc, Kind: KindSend, TurnID: "person-turn", Message: "person"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("an ask took a reserved slot: %v", err)
	}
	if n := f.openAsks(f.cc); n != 0 {
		t.Fatalf("a refused ask stayed open: %d", n)
	}
	for i := 0; i < AskPersonReserve; i++ {
		if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: f.rc, Kind: KindSend, TurnID: "person-turn", Message: "person"}); err != nil {
			t.Fatalf("person send %d: %v", i, err)
		}
	}
}
