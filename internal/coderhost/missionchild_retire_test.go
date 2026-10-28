package coderhost

import (
	"testing"
	"time"
)

// A person's follow-up that arrives after retireIdle looked, and before it wrote, keeps the session alive.
func TestRetireIdleDoesNotStopSessionWithNewQueuedWork(t *testing.T) {
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	if _, err := f.st.End("p", out.ConversationID, EndDone); err != nil {
		t.Fatal(err)
	}
	arrived := false
	f.st.now = func() time.Time {
		f.st.now = time.Now // the enqueue below reads the clock too
		arrived = true
		if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: out.ConversationID, Kind: KindSend, TurnID: "operator-followup", Message: "Continue this session"}); err != nil {
			t.Error(err)
		}
		return time.Now()
	}
	f.h.retireIdle("p", out.ConversationID)
	if !arrived {
		t.Fatal("the concurrent follow-up did not run")
	}
	if x := state(t, f.st, out.ConversationID); x.State == StateStopped {
		t.Fatalf("retired a session that has a queued follow-up (pending=%d): %+v", f.st.PendingFor("p", out.ConversationID), x)
	}
}

// A recorded live turn process keeps the session: the host adopts a positive turn pid even when no turn id is named.
func TestRetireIdleDoesNotStopARecordedLivePID(t *testing.T) {
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	if _, err := f.st.End("p", out.ConversationID, EndDone); err != nil {
		t.Fatal(err)
	}
	x := state(t, f.st, out.ConversationID)
	x.TurnID, x.TurnPID, x.TurnStamp = "", 4242, "start:1"
	if err := f.st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	f.h.retireIdle("p", out.ConversationID)
	if got := state(t, f.st, out.ConversationID); got.State == StateStopped {
		t.Fatalf("retired a session that records a live turn process: %+v", got)
	}
}
