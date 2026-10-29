package coderhost

import (
	"errors"
	"strings"
	"testing"
)

// coderTree puts the askTree's chats into the coder host's own tree: rc > pc > cc > gc, so session_send can
// reach a descendant the way the master tools do.
func (f *askTree) coderTree(t *testing.T) {
	t.Helper()
	for child, parent := range map[string]string{f.pc: f.rc, f.cc: f.pc, f.gc: f.cc} {
		if _, err := f.st.db.Exec(`UPDATE coder_sessions SET role = 'child', parent_conversation_id = ? WHERE conversation_id = ?`, parent, child); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *askTree) askState(t *testing.T, id int64) string {
	t.Helper()
	a, err := f.st.GetAsk("p", id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Status
}

// Finding 1: an ancestor's session_send is a command even while an ask addressed to it is open.
func TestSessionSendIsAlwaysACommandEvenWithAnOpenAsk(t *testing.T) {
	f := newAskTree(t)
	f.coderTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := f.st.SendToChild(Caller{ProfileID: "p", ConversationID: f.rc}, f.cc, "STOP, revert commit abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd.TurnID, MasterTurnPrefix) || cmd.Issuer != f.rc || cmd.Message != "STOP, revert commit abc" {
		t.Fatalf("the order was turned into something else: %+v", cmd)
	}
	if st := f.askState(t, got.AskID); st != askOpen {
		t.Fatalf("session_send closed the ask: %s", st)
	}
}

func TestTheAskTellsTheAncestorHowToReply(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	m := f.commandsTo(f.rc)[0].Message
	if !strings.Contains(m, "session_reply_ask") || !strings.Contains(m, "ask_id="+itoa(got.AskID)) {
		t.Fatalf("no way to reply named:\n%s", m)
	}
}

func TestReplyAskRefusals(t *testing.T) {
	f := newAskTree(t)
	norm, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	f.tick()
	crit, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	asker := f.lead(f.cc)
	cases := []struct {
		name string
		sc   CallerScope
		id   int64
		want error
	}{
		{"unknown ask", f.lead(f.rc), norm.AskID + 99, ErrNotYours},
		{"the parent is not the addressee", f.lead(f.pc), norm.AskID, ErrNotYours},
		{"the asker cannot answer itself", asker, norm.AskID, ErrNotYours},
		{"a critical ask is the person's", f.lead(f.rc), crit.AskID, ErrNotYours},
		{"a bound worker", NewLeadScope(Caller{ProfileID: "p", ConversationID: f.rc}, BindingTask), norm.AskID, nil},
	}
	for _, c := range cases {
		_, err := f.st.ReplyAsk(c.sc, "p", c.id, "answer")
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "q", norm.AskID, "answer"); err == nil {
		t.Error("another profile replied")
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", norm.AskID, strings.Repeat("x", MaxAnswerBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Errorf("an oversize reply: %v", err)
	}
	if len(f.commandsTo(f.cc)) != 0 || f.askState(t, norm.AskID) != askOpen || f.askState(t, crit.AskID) != askOpen {
		t.Fatal("a refused reply changed something")
	}
}

func TestReplyAskIsRefusedOnceExpired(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_session_asks SET status = ? WHERE id = ?`, askExpired, got.AskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "late"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("%v", err)
	}
}

func TestReplyAskRefusesAnAskerThatMovedToAnotherAncestor(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	// Now C hangs directly under the foreign mission X: the root is no longer above it.
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, f.X.ID, f.C.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "x"); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("%v", err)
	}
}

// Finding 3: every error path of the reply tool refuses and leaves the ask open.
func TestReplyAskFailsClosedOnStoreErrors(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	// The tree cannot be read (a cycle above the asker): refused, not answered, and not voided either.
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, f.C.ID, f.R.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "x"); err == nil {
		t.Fatal("a reply went through while the tree could not be read")
	}
	if f.askState(t, got.AskID) != askOpen || len(f.commandsTo(f.cc)) != 0 {
		t.Fatal("a failed tree read answered or voided the ask")
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET parent_session_id = NULL WHERE id = ?`, f.R.ID); err != nil {
		t.Fatal(err)
	}
	// The ask cannot be read.
	if _, err := f.st.db.Exec(`ALTER TABLE coder_session_asks RENAME TO coder_session_asks_x`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "x"); err == nil || errors.Is(err, ErrNotYours) {
		t.Fatalf("a read error must be an error, not a not-addressee: %v", err)
	}
	if len(f.commandsTo(f.cc)) != 0 {
		t.Fatal("a reply was queued although the ask could not be read")
	}
}

// A reply that cannot be queued puts the ask back to open.
func TestReplyAskReopensTheAskWhenTheReplyCannotBeQueued(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`ALTER TABLE coder_session_commands RENAME TO coder_session_commands_x`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "x"); err == nil {
		t.Fatal("no error")
	}
	if f.askState(t, got.AskID) != askOpen {
		t.Fatal("the ask stayed closed with nothing delivered")
	}
}

// A lead that is not the addressee of an ask gets not_yours and the ask stays open and undelivered,
// whichever other lead it is: a sibling of the asker's parent, the asker's parent, or the lead of
// another mission. The addressee check is the ask's own ancestor_conv, not the position in the tree.
func TestASecondLeadIsRefusedAsNotTheAddresseeAndTheAskStaysOpen(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	for name, conv := range map[string]string{"sibling lead": f.s, "parent lead": f.pc, "other mission lead": f.X.ConversationID} {
		if _, err := f.st.ReplyAsk(f.lead(conv), "p", got.AskID, "answer"); !errors.Is(err, ErrNotYours) {
			t.Errorf("%s: want not_yours, got %v", name, err)
		}
	}
	if st := f.askState(t, got.AskID); st != askOpen {
		t.Fatalf("a refused lead changed the ask: %s", st)
	}
	if n := len(f.commandsTo(f.cc)); n != 0 {
		t.Fatalf("a refused lead delivered %d messages to the asker", n)
	}
	// The addressee still answers it afterwards.
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "answer"); err != nil {
		t.Fatalf("the addressee: %v", err)
	}
}
