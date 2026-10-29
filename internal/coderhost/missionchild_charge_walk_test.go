package coderhost

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Third review of the launcher (P1-1, P1-2, P2): a table of the states a child's cost can be in, walked against the
// invariants listed at the top of missionchild_charge.go.
//
//	how the row knows the conversation : child id | pending id only
//	where the cost is                  : snapshot (a delete wrote it) | live events (never deleted) | lost (a bypass delete)
//	what became of the row             : gone by the delete | ended first, deleted later | released (marked again, link cleared)
//	the charge                         : none yet | written
//	the spawn                          : normal | failed (cleanup deleted the conversation) | undo (release)

type walkCase struct {
	name        string
	pendingOnly bool   // the row names the conversation as pending only (the first message ran before the link)
	cost        string // "snapshot", "live" or "lost"
	endFirst    bool   // the host ended the row (as done) before the conversation was deleted
	release     bool   // the cleanup's release marks the row again after the delete, clearing the link
	failedRel   bool   // the release is the failed-spawn one (the attempt ends as failed), not the refused one
	spawning    bool   // an older binary's delete leaves a pending-only row spawning (it does not mark it gone)
	hostFirst   bool   // the host's pass runs between the delete and the release (round 3c P2-3)
	policyUp    bool   // the share and the default budget are raised between the delete and the host pass (round 3c P1-2)
	unresolved  bool   // expected: nobody can know the cost
	recoverable bool   // expected: the meter ends at the real cost
}

var walkCases = []walkCase{
	{name: "child id, deleted, snapshot", cost: "snapshot", recoverable: true},
	{name: "pending only, deleted, snapshot (P1-1)", pendingOnly: true, cost: "snapshot", recoverable: true},
	{name: "child id, ended then deleted, snapshot", cost: "snapshot", endFirst: true, recoverable: true},
	{name: "pending only, deleted then released, snapshot", pendingOnly: true, cost: "snapshot", release: true, recoverable: true},
	{name: "child id, deleted then released, snapshot", cost: "snapshot", release: true, recoverable: true},
	{name: "child id, ended, live events", cost: "live", recoverable: true},
	{name: "child id, bypass delete, lost (P1-2)", cost: "lost", unresolved: true},
	{name: "pending only, bypass delete, lost (P1-2)", pendingOnly: true, cost: "lost", unresolved: true},
	{name: "child id, ended then bypass delete, lost", cost: "lost", endFirst: true, unresolved: true},
	{name: "pending only, bypass delete, row left spawning, failed-spawn release (round 3b P1-2)", pendingOnly: true, cost: "lost", spawning: true, release: true, failedRel: true, unresolved: true},
	{name: "pending only, bypass delete, row left spawning, refused-carve release (round 3b P1-2)", pendingOnly: true, cost: "lost", spawning: true, release: true, unresolved: true},
	{name: "pending only, bypass delete, row marked gone, failed-spawn release (round 3b P1-2)", pendingOnly: true, cost: "lost", release: true, failedRel: true, unresolved: true},
	{name: "pending only, bypass delete, row marked gone, refused-carve release (round 3b P1-2)", pendingOnly: true, cost: "lost", release: true, unresolved: true},
	{name: "pending only, row left spawning, host first, then refused-carve release (round 3c P2-3)", pendingOnly: true, cost: "lost", spawning: true, hostFirst: true, release: true, unresolved: true},
	{name: "pending only, row marked gone, host first, then refused-carve release (round 3c P2-3)", pendingOnly: true, cost: "lost", hostFirst: true, release: true, unresolved: true},
	{name: "child id, bypass delete, policy raised before the host pass (round 3c P1-2)", cost: "lost", policyUp: true, unresolved: true},
	{name: "pending only, row left spawning, policy raised before the host pass (round 3c P1-1/2)", pendingOnly: true, cost: "lost", spawning: true, policyUp: true, unresolved: true},
}

const walkCost = 1.25

// buildWalk puts the fixture's task 0 in the state of the case and returns the conversation.
func (f *hostFix) buildWalk(t *testing.T, c walkCase) string {
	t.Helper()
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	conv := f.endedChild(t, 0, walkCost)
	if c.cost == "live" {
		return conv
	}
	if c.endFirst {
		f.h.ChildTokens = &fakeRevoker{err: fmt.Errorf("db busy")} // the row ends, nothing is charged yet
		f.h.missionChildPass()
		if r := f.row(t, 0); r.State != ChildEnded {
			t.Fatalf("row = %+v", r)
		}
		f.h.ChildTokens = nil
	}
	if c.pendingOnly {
		if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET state = 'spawning', pending_conversation_id = child_conversation_id, child_conversation_id = '', spawned_at = '', generation = 7
			WHERE task_id = ?`, f.ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET generation = 7 WHERE task_id = ?`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	f.deleteChild(t, conv, c.cost == "snapshot")
	if c.pendingOnly && c.cost == "lost" && !c.spawning {
		// an older binary's delete does not mark the row either
		if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET state = 'gone', end_reason = 'deleted' WHERE task_id = ?`, f.ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	if c.policyUp {
		f.setPolicy(t, f.scope(), PolicyPatch{SharePct: ip(80), DefaultBudgetUSD: fp(8)})
	}
	if c.hostFirst {
		f.passes(2)
	}
	if c.release {
		if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], 7, f.lead.ConversationID, c.failedRel, "undo"); err != nil {
			t.Fatal(err)
		}
	}
	return conv
}

func (f *hostFix) passes(n int) {
	for i := 0; i < n; i++ {
		f.h.missionChildPass()
		f.h.settleEnded()
	}
	(&Host{Store: f.st, L: newFakeLauncher()}).missionChildPass() // a second host over the same store
}

// reserveSecondTaskIsRefused marks task 1 with the whole mission's budget and shows that Reserve refuses it.
func (f *hostFix) reserveSecondTaskIsRefused(t *testing.T) {
	t.Helper()
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{BudgetUSD: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("Reserve of $10 against a mission with a deleted child's cost unsettled = %v, want ErrChildrenFull", err)
	}
}

func (f *hostFix) markable(t *testing.T) error {
	t.Helper()
	_, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 2})
	return err
}

func TestTheStatesAChildsCostCanBeInHoldTheInvariants(t *testing.T) {
	for _, c := range walkCases {
		t.Run(c.name, func(t *testing.T) {
			f := newHostFix(t)
			conv := f.buildWalk(t, c)
			if c.recoverable && c.release {
				// the interleaving of the review: the cost is on a snapshot only, the row no longer names the conversation
				if r := f.row(t, 0); r.State != ChildMarked || r.PendingConversationID != "" || (c.pendingOnly && r.ChildConversationID != "") {
					t.Fatalf("row after the release = %+v", r)
				}
			}
			if c.cost != "live" {
				// the interval between the delete and the host's pass: the cost is still held (review of round 3, P1)
				if why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 10); err != nil || why == "" {
					t.Fatalf("before any pass a carve of the whole $10 mission passed (%q, %v): the deleted child's cost is not held", why, err)
				}
				f.reserveSecondTaskIsRefused(t)
			}
			early := f.held(t)
			if c.cost == "lost" && early != 10 { // round 3c: one lost child holds the whole remainder, once (no reservation added on top)
				t.Fatalf("held = %v, want the mission's whole remainder 10", early)
			}
			if c.cost == "lost" && c.release {
				// round 3b P1-2: the release never clears the identity of a row whose cost nothing records
				if r := f.row(t, 0); r.ident() != conv || r.State != ChildFailed || len(f.eventsOfKind(t, "unresolved")) != 1 {
					t.Fatalf("after the release of a lost child: row = %+v, unresolved = %v", r, f.eventsOfKind(t, "unresolved"))
				}
			}
			f.passes(4)
			if c.cost == "lost" {
				// invariant (v): the hold of a row never grows between the delete and its resolution
				if got := f.held(t); got != early {
					t.Fatalf("the hold went from %v (before the pass) to %v (after it)", early, got)
				}
			}
			charges := f.eventsOfKind(t, "charge")
			switch {
			case c.recoverable:
				// (i) the meter is at the real cost, (ii) once
				if got := f.missionSpent(t); got != walkCost {
					t.Fatalf("meter = %v, want %v", got, walkCost)
				}
				if len(charges) != 1 || !strings.HasPrefix(charges[0], conv+" ") {
					t.Fatalf("charge rows = %v, want one for %s", charges, conv)
				}
				if err := f.markable(t); err != nil { // (iv) charged: the task can be marked again
					t.Fatalf("mark after the charge: %v", err)
				}
			case c.unresolved:
				// (iii) not settled, not guessed, held; (iv) Mark refused
				if got := f.missionSpent(t); got != 0 {
					t.Fatalf("meter = %v: an unknown cost was guessed", got)
				}
				if len(charges) != 0 {
					t.Fatalf("charge rows = %v: an unknown cost was settled", charges)
				}
				if ev := f.eventsOfKind(t, "unresolved"); len(ev) != 1 {
					t.Fatalf("unresolved rows = %v, want one", ev)
				}
				if err := f.markable(t); !errors.Is(err, ErrChildUnsettled) {
					t.Fatalf("mark of an unresolved row = %v, want ErrChildUnsettled", err)
				}
				if why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 7); err != nil || why == "" {
					t.Fatalf("a carve of 7 against a mission of 10 passed (%q, %v): the unknown cost is not held", why, err)
				}
				// (a) only the person resolves it; (ii) once
				if _, err := f.st.ResolveCharge("p", conv, walkCost, false, "read from the provider's bill"); err != nil {
					t.Fatal(err)
				}
				f.passes(2)
				if _, err := f.st.ResolveCharge("p", conv, walkCost, false, ""); err == nil {
					t.Fatal("a second resolution was accepted")
				}
				if got := f.missionSpent(t); got != walkCost {
					t.Fatalf("meter after the resolution = %v, want %v", got, walkCost)
				}
				if why, _ := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 7); why != "" {
					t.Fatalf("the hold outlived the resolution: %s", why)
				}
				if err := f.markable(t); err != nil {
					t.Fatalf("mark after the resolution: %v", err)
				}
			}
		})
	}
}

// P1-1 end to end: the real order of a spawn, a turn that ran on the pending conversation, and the conversation's
// deletion by the cleanup before the link.
func TestAPendingConversationThatRanAndIsDeletedBeforeTheLinkIsCharged(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
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
	f.ranTurn(t, out.ConversationID)
	event(t, f.st, out.ConversationID, "t-"+out.ConversationID, 1, "usage.updated", fmt.Sprintf(`{"agentId":"","costUsd":%v}`, walkCost))
	// the cleanup of a failed spawn deletes the conversation, then the release runs
	_ = f.st.abandonSpawn(f.lead, "p", f.se.ID, f.ids[0], res.Generation, out.ConversationID, errors.New("the link failed"))
	f.passes(3)
	if got := f.missionSpent(t); got != walkCost {
		t.Fatalf("meter = %v, want %v (snapshots %v)", got, walkCost, f.eventsOfKind(t, "spend"))
	}
	if n := f.chargeRows(t); n != 1 {
		t.Fatalf("%d charge rows, want 1", n)
	}
}

// P2: a charge record that cannot be written leaves the meter alone, and the retry adds the cost once.
func TestAFailedChargeRecordLeavesTheMeterAndTheRetryChargesOnce(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, walkCost)
	f.deleteChild(t, conv, true)
	if _, err := f.st.db.Exec(`CREATE TRIGGER refuse_charge BEFORE INSERT ON coder_mission_events WHEN NEW.kind = 'charge' BEGIN SELECT RAISE(ABORT, 'no charge rows'); END`); err != nil {
		t.Fatal(err)
	}
	f.passes(3)
	if got := f.missionSpent(t); got != 0 {
		t.Fatalf("meter = %v after a charge record that could not be written: the spend was committed alone", got)
	}
	if _, err := f.st.db.Exec(`DROP TRIGGER refuse_charge`); err != nil {
		t.Fatal(err)
	}
	f.passes(3)
	if got := f.missionSpent(t); got != walkCost {
		t.Fatalf("meter = %v, want %v once", got, walkCost)
	}
	if n := f.chargeRows(t); n != 1 {
		t.Fatalf("%d charge rows, want 1", n)
	}
}

// Two writers racing for the same child (the host and the person's verb): the second finds the charge row inside its own transaction.
func TestTheChargeTransactionRefusesToAddASecondTime(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, walkCost)
	f.h.missionChildPass()
	if usd, err := f.st.writeCharge("p", f.se.ID, f.ids[0], conv, walkCost, conv+" $1.250000", "host"); err != nil || usd != 0 {
		t.Fatalf("second writeCharge = %v, %v", usd, err)
	}
	if got := f.missionSpent(t); got != walkCost {
		t.Fatalf("meter = %v", got)
	}
}

// A skip is a resolution: no money, the person's reason on the row, the task markable again.
func TestAnUnresolvedChildCanBeSkippedWithAReason(t *testing.T) {
	f := newHostFix(t)
	conv := f.buildWalk(t, walkCases[6])
	f.passes(2)
	if _, err := f.st.ResolveCharge("p", conv, 0, true, ""); err == nil {
		t.Fatal("a skip without a reason was accepted")
	}
	if _, err := f.st.ResolveCharge("p", conv, 0, true, "free tier, nothing billed"); err != nil {
		t.Fatal(err)
	}
	ev := f.eventsOfKind(t, "charge")
	if len(ev) != 1 || !strings.Contains(ev[0], "skipped by the operator: free tier") {
		t.Fatalf("charge rows = %v", ev)
	}
	if got := f.missionSpent(t); got != 0 {
		t.Fatalf("meter = %v", got)
	}
	if err := f.markable(t); err != nil {
		t.Fatal(err)
	}
}

func TestResolveChargeRefusesAChildThatIsNotUnresolved(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, walkCost)
	f.h.missionChildPass()
	if _, err := f.st.ResolveCharge("p", conv, 3, false, ""); err == nil {
		t.Fatal("resolved a child that was charged normally")
	}
	if _, err := f.st.ResolveCharge("p", conv, -1, false, ""); err == nil {
		t.Fatal("a negative amount was accepted")
	}
}

// A mission child with an unset row budget and an uncapped lead still holds something against a budgeted mission.
func TestAnUnresolvedChildWithNoBudgetOfItsOwnHoldsTheWholeRemainder(t *testing.T) {
	f := newHostFix(t)
	conv := f.buildWalk(t, walkCases[6])
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET budget_usd = 0 WHERE task_id = ?`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	f.passes(2)
	ev := f.eventsOfKind(t, "unresolved")
	if len(ev) != 1 || !strings.HasPrefix(ev[0], conv+" $") {
		t.Fatalf("unresolved rows = %v", ev)
	}
	if why, _ := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 0.5); why == "" {
		t.Fatalf("a carve of 0.5 passed with a $%s hold: %v", "?", ev)
	}
}

// held is what the mission says its children hold against its budget (the figure missionBudgetRefusal reports).
func (f *hostFix) held(t *testing.T) float64 {
	t.Helper()
	why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 1000)
	if err != nil || why == "" {
		t.Fatalf("a carve of 1000 against a mission of 10 passed (%q, %v)", why, err)
	}
	var carve, left, held float64
	if _, err := fmt.Sscanf(why, "a carve of $%f would pass the mission's remaining budget ($%f left, $%f held by child sessions)", &carve, &left, &held); err != nil {
		t.Fatalf("%q: %v", why, err)
	}
	return held
}

// Round 3c: a lost child holds the WHOLE remainder of its mission, from the first moment and whatever the policy
// says later, so a small reservation is refused beside it and the hold never moves.
func TestALostChildHoldsTheWholeRemainderWhateverThePolicySaysLater(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.setPolicy(t, f.scope(), PolicyPatch{DefaultBudgetUSD: fp(1)})
	conv := f.endedChild(t, 0, walkCost) // its row's wish is $4
	f.deleteChild(t, conv, false)
	early := f.held(t)
	if early != 10 {
		t.Fatalf("early hold = %v, want the whole remainder 10 (not the wish 4 or the default share 1)", early)
	}
	f.reserveSecondTaskIsRefusedAt(t, 1)
	// a lead raises the share and the default before the host's pass (round 3c P1-2): nothing grows, nothing passes
	f.setPolicy(t, f.scope(), PolicyPatch{SharePct: ip(80), DefaultBudgetUSD: fp(8)})
	f.reserveSecondTaskIsRefusedAt(t, 1)
	f.passes(3)
	if ev := f.eventsOfKind(t, "unresolved"); len(ev) != 1 {
		t.Fatalf("unresolved rows = %v, want one", ev)
	}
	if got := f.held(t); got != early {
		t.Fatalf("held went from %v to %v across the policy change and the pass", early, got)
	}
	f.reserveSecondTaskIsRefusedAt(t, 1)
}

// Two lost children hold the remainder once, not twice (max, not sum), and a spawning reservation beside them adds nothing.
func TestTwoLostChildrenHoldTheRemainderOnce(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	a := f.endedChild(t, 0, walkCost)
	b := f.endedChild(t, 1, walkCost)
	f.deleteChild(t, a, false)
	f.deleteChild(t, b, false)
	f.passes(3)
	if ev := f.eventsOfKind(t, "unresolved"); len(ev) != 2 {
		t.Fatalf("unresolved rows = %v, want one per child", ev)
	}
	if got := f.held(t); got != 10 {
		t.Fatalf("held = %v, want 10 (the remainder once)", got)
	}
}

// Round 3c P1-1: a pending-only row an older binary's delete left `spawning` holds the whole remainder at once; the
// release that follows (refused carve) adds no second hold, and the total never exceeds the mission's budget.
func TestALostChildLeftSpawningHoldsTheRemainderNotItsReservation(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.setPolicy(t, f.scope(), PolicyPatch{DefaultBudgetUSD: fp(8)})
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 4}); err != nil {
		t.Fatal(err)
	}
	row, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET pending_conversation_id = 'vanished-conv' WHERE task_id = ?`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if got := f.held(t); got != 10 {
		t.Fatalf("held with a lost spawning row = %v, want 10 (the $4 reservation is subsumed)", got)
	}
	f.reserveSecondTaskIsRefusedAt(t, 6)
	if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], row.Generation, f.lead.ConversationID, false, "undo"); err != nil {
		t.Fatal(err)
	}
	if got := f.held(t); got != 10 {
		t.Fatalf("held after the release = %v, want 10", got)
	}
	f.reserveSecondTaskIsRefusedAt(t, 6)
}

// Round 3c P2-3 and P2-4: the host's record and the release's record are one record, and the release never clears
// the identity of a row the host already wrote it for.
func TestHostFirstThenReleaseKeepsTheRowLinkedWithOneRecord(t *testing.T) {
	for _, spawning := range []bool{false, true} {
		t.Run(fmt.Sprintf("spawning=%v", spawning), func(t *testing.T) {
			f := newHostFix(t)
			c := walkCase{pendingOnly: true, cost: "lost", spawning: spawning}
			conv := f.buildWalk(t, c)
			f.passes(2) // the host writes the unresolved record first
			if ev := f.eventsOfKind(t, "unresolved"); len(ev) != 1 {
				t.Fatalf("after the host: unresolved = %v", ev)
			}
			if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], 7, f.lead.ConversationID, false, "undo"); err != nil {
				t.Fatal(err)
			}
			if r := f.row(t, 0); r.ident() != conv || r.State == ChildMarked {
				t.Fatalf("after the release: row = %+v, want it linked to %s and not marked", r, conv)
			}
			if ev := f.eventsOfKind(t, "unresolved"); len(ev) != 1 {
				t.Fatalf("after the release: unresolved = %v, want still one", ev)
			}
			if err := f.markable(t); !errors.Is(err, ErrChildUnsettled) {
				t.Fatalf("mark before the resolution = %v, want ErrChildUnsettled", err)
			}
		})
	}
}

// The once-only write: whoever writes second adds nothing.
func TestTheUnresolvedRecordIsWrittenOnce(t *testing.T) {
	f := newHostFix(t)
	for i := 0; i < 3; i++ {
		if _, err := insertUnresolvedOnce(f.st.db, "p", f.se.ID, f.ids[0], "conv-x", "conv-x $1 unresolved: test", "t"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.markUnresolved("p", chargeTarget{session: f.se.ID, task: f.ids[0]}, "conv-x", "again"); err != nil {
		t.Fatal(err)
	}
	if ev := f.eventsOfKind(t, "unresolved"); len(ev) != 1 {
		t.Fatalf("unresolved rows = %v, want one", ev)
	}
}

// reserveSecondTaskIsRefusedAt marks task 1 with a budget of usd and shows that Reserve refuses it.
func (f *hostFix) reserveSecondTaskIsRefusedAt(t *testing.T, usd float64) {
	t.Helper()
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{BudgetUSD: usd}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("Reserve of $%v beside a lost child = %v, want ErrChildrenFull", usd, err)
	}
}
