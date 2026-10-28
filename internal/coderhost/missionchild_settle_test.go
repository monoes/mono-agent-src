package coderhost

import (
	"fmt"
	"strings"
	"testing"
)

// A settlement that failed is not lost when the lead marks the task again (security review of the launcher).

func TestARemarkDoesNotLoseTheSettlementOfTheEarlierChild(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{err: fmt.Errorf("db busy")}
	f.h.ChildTokens = rv
	conv := f.endedChild(t, 0, 1.5)
	f.h.missionChildPass() // the row ends; the revoke fails, so nothing is charged
	if r := f.row(t, 0); r.State != ChildEnded || r.ChildConversationID != conv || f.missionSpent(t) != 0 {
		t.Fatalf("row %+v spent %v", r, f.missionSpent(t))
	}
	// the lead marks the task again before a later pass succeeded: refused, and the old conversation stays findable
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 2}); err == nil || !strings.Contains(err.Error(), "settled") {
		t.Fatalf("re-mark of an unsettled child: %v", err)
	}
	if r := f.row(t, 0); r.ChildConversationID != conv {
		t.Fatalf("the re-mark forgot the conversation: %+v", r)
	}
	rv.mu.Lock()
	rv.err = nil
	rv.mu.Unlock()
	f.h.missionChildPass()
	f.h.missionChildPass()
	if got := f.missionSpent(t); got != 1.5 || f.chargeRows(t) != 1 {
		t.Fatalf("spent %v with %d charge rows, want 1.5 once", got, f.chargeRows(t))
	}
	if rv.err != nil || !rv.revoked(conv) {
		t.Fatalf("the old child's token was not revoked: %v", rv.calls)
	}
	// settled: the task can be marked again
	if r, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 2}); err != nil || r.State != ChildMarked {
		t.Fatalf("re-mark after the settlement: %+v %v", r, err)
	}
}

func TestAChargeThatCannotBeLookedUpIsNotTakenForNotCharged(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1)
	f.h.missionChildPass()
	if f.missionSpent(t) != 1 {
		t.Fatal("not charged")
	}
	if _, err := f.st.db.Exec(`ALTER TABLE coder_mission_events RENAME TO coder_mission_events_away`); err != nil {
		t.Fatal(err)
	}
	if usd, err := f.st.ChargeChild("p", conv); err == nil || usd != 0 {
		t.Fatalf("ChargeChild with the event table unreadable = %v, %v: it must be an error", usd, err)
	}
	if _, err := f.st.db.Exec(`ALTER TABLE coder_mission_events_away RENAME TO coder_mission_events`); err != nil {
		t.Fatal(err)
	}
	if got := f.missionSpent(t); got != 1 {
		t.Fatalf("spent = %v: charged again on a database error", got)
	}
}

func TestALiveChildWithoutATurnHasItsTokenRevokedOnEveryPass(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{err: fmt.Errorf("db busy")}
	f.h.ChildTokens = rv
	out := f.idleTaskChild(t, 0, 3)
	if _, err := f.st.db.Exec(`UPDATE coder_sessions SET state = 'waiting_user', turn_id = '' WHERE conversation_id = ?`, out.ConversationID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	f.h.missionChildPass()
	rv.mu.Lock()
	n := len(rv.calls)
	rv.mu.Unlock()
	if n < 2 {
		t.Fatalf("%d revoke attempts over two passes: a failed revoke was not retried", n)
	}
	if r := f.row(t, 0); r.State != ChildSpawned {
		t.Fatalf("row = %+v: the revoke retry must not end the child", r)
	}
}

func TestARunningTurnsTokenIsNotRevokedByThePass(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{}
	f.h.ChildTokens = rv
	out := f.task(t, 0, 3)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if rv.revoked(out.ConversationID) {
		x, _, _ := f.st.GetSession("p", out.ConversationID)
		t.Fatalf("a pass revoked the token of a running turn: %v %+v", rv.calls, x)
	}
	f.finish()
}
