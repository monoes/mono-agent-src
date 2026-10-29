package coderhost

import (
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderspend"
)

// Review of 4c37ace23: the spend snapshot is not opt-in, the "assumed" fallback is bounded, and a spawn that never
// ran is not charged.

// A plain DeleteConversation (no callback) records the spend, in the format snapshotOf reads.
func TestAPlainDeleteRecordsTheChildsSpendWithoutAnyCallback(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1.25)
	f.deleteChild(t, conv, true)
	ev := f.eventsOfKind(t, "spend")
	if len(ev) != 1 || ev[0] != coderspend.Note(conv, 1.25) {
		t.Fatalf("spend rows = %v, want one %q", ev, coderspend.Note(conv, 1.25))
	}
	if usd, found, err := f.st.snapshotOf("p", f.se.ID, conv); err != nil || !found || usd != 1.25 {
		t.Fatalf("snapshotOf = %v %v %v", usd, found, err)
	}
}

// A snapshot that cannot be written aborts the delete: the conversation and its usage stay.
func TestADeleteWhoseSnapshotFailsIsRolledBack(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1.25)
	if _, err := f.st.db.Exec(`CREATE TRIGGER refuse_spend BEFORE INSERT ON coder_mission_events WHEN NEW.kind = 'spend' BEGIN SELECT RAISE(ABORT, 'no spend rows'); END`); err != nil {
		t.Fatal(err)
	}
	store, _ := ai.NewAIStore(f.st.db)
	if err := store.DeleteConversation(conv, "p"); err == nil {
		t.Fatal("the delete went through without recording the spend")
	}
	if got, _ := f.st.Spent("p", conv); got != 1.25 {
		t.Fatalf("the usage after the failed delete = %v, want 1.25", got)
	}
}

// A wish is any finite number: the assumed charge never exceeds what the mission has left of its budget.
func TestTheAssumedChargeIsBoundedByTheMissionsRemainingBudget(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1)
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET budget_usd = 1000000000 WHERE child_conversation_id = ?`, conv); err != nil {
		t.Fatal(err)
	}
	f.deleteChild(t, conv, false)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10, spent_usd = 2 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	f.h.missionChildPass()
	if got := f.missionSpent(t); got != 10 {
		t.Fatalf("mission spent = %v, want 10 (2 + the 8 left)", got)
	}
	if ev := f.eventsOfKind(t, "charge"); len(ev) != 1 || !strings.Contains(ev[0], "assumed") {
		t.Fatalf("charge row = %v", ev)
	}
}

// A row that recorded no budget is charged the share the mission would have given it, not nothing.
func TestAnAssumedChargeForARowWithoutABudgetUsesTheDerivedShare(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1)
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET budget_usd = 0 WHERE child_conversation_id = ?`, conv); err != nil {
		t.Fatal(err)
	}
	pol, err := f.st.GetPolicy("p", f.se.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := f.st.DefaultChildBudget("p", f.se.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.deleteChild(t, conv, false)
	f.h.missionChildPass()
	f.h.missionChildPass()
	got := f.missionSpent(t)
	if want > 0 && got != want {
		t.Fatalf("mission spent = %v, want the derived share %v (policy %+v)", got, want, pol)
	}
	if ev := f.eventsOfKind(t, "charge"); len(ev) != 1 || !(strings.Contains(ev[0], "assumed") || strings.Contains(ev[0], "spend unknown")) {
		t.Fatalf("charge row = %v: the gap must be written down", ev)
	}
}

// The note also covers a row that ended some other way before its conversation went.
func TestAnEndedRowWhoseConversationWasBypassDeletedIsNotChargedInSilence(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{err: fmt.Errorf("db busy")}
	f.h.ChildTokens = rv
	conv := f.endedChild(t, 0, 1.25)
	f.h.missionChildPass() // ends as done (not deleted); the revoke fails, so nothing is charged
	if r := f.row(t, 0); r.State != ChildEnded {
		t.Fatalf("row = %+v", r)
	}
	f.deleteChild(t, conv, false)
	rv.mu.Lock()
	rv.err = nil
	rv.mu.Unlock()
	f.h.missionChildPass()
	if ev := f.eventsOfKind(t, "charge"); len(ev) != 1 || !(strings.Contains(ev[0], "assumed") || strings.Contains(ev[0], "spend unknown")) {
		t.Fatalf("charge row = %v: a bypass-deleted ended row was charged without a note", ev)
	}
	if got := f.missionSpent(t); got <= 0 {
		t.Fatalf("mission spent = %v: an ended child with 1.25 of usage was charged nothing", got)
	}
}

// A gone row whose child never started is charged nothing, and the note says why.
func TestAGoneRowThatNeverStartedIsChargedNothingAndSaysSo(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 0)
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET spawned_at = '' WHERE child_conversation_id = ?`, conv); err != nil {
		t.Fatal(err)
	}
	f.deleteChild(t, conv, false)
	f.h.missionChildPass()
	f.h.missionChildPass()
	if got := f.missionSpent(t); got != 0 {
		t.Fatalf("mission spent = %v for a spawn that never ran", got)
	}
	if ev := f.eventsOfKind(t, "charge"); len(ev) != 1 || !strings.Contains(ev[0], "never ran") {
		t.Fatalf("charge row = %v", ev)
	}
}

// The host's sweep runs between the cleanup's delete and its release of the reservation: it charges nothing, and
// the release leaves a row with no conversation link.
func TestTheSweepBetweenTheCleanupDeleteAndTheReleaseChargesNothing(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 4}); err != nil {
		t.Fatal(err)
	}
	res, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.st.Spawn(f.lead, SpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], out.ConversationID); err != nil {
		t.Fatal(err)
	}
	// the state of the review's interleaving: the row also names the conversation as its child
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET child_conversation_id = ? WHERE task_id = ?`, out.ConversationID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	f.st.afterCleanupDelete = func() {
		f.h.settleEnded()
		if got := f.missionSpent(t); got != 0 {
			t.Errorf("the sweep between the delete and the release charged %v for a spawn that never ran", got)
		}
	}
	_ = f.st.abandonSpawn(f.lead, "p", f.se.ID, f.ids[0], res.Generation, out.ConversationID, errCarveRefused)
	if got := f.missionSpent(t); got != 0 {
		t.Fatalf("mission spent = %v", got)
	}
	if r := f.row(t, 0); r.State != ChildMarked || r.ChildConversationID != "" || r.PendingConversationID != "" {
		t.Fatalf("row after the release = %+v, want marked with no conversation link", r)
	}
}
