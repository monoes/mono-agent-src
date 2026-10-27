package coderhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// boundedly runs fn and fails the test, instead of hanging it, when fn does not return in d.
func boundedly(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not return within %s", d)
	}
}

func renameTable(t *testing.T, st *Store, from, to string) {
	t.Helper()
	if _, err := st.db.Exec(`ALTER TABLE ` + from + ` RENAME TO ` + to); err != nil {
		t.Fatal(err)
	}
}

func (f *mcFix) capOf(t *testing.T, conv string) float64 {
	t.Helper()
	b, err := f.st.Budget("p", conv)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// orphanFix reserves task 0, creates the spawn's child with a carve of 4 under a lead budget of 10, notes it
// as pending and lets the reservation go stale.
func orphanFix(t *testing.T) (*mcFix, string) {
	t.Helper()
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	made, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], made.ConversationID); err != nil {
		t.Fatal(err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(10 * time.Minute) }
	return f, made.ConversationID
}

// N1: the state change, the audit row and the lowering of the cap stand or fall together, and a failed pass
// is retried by the next one.
func TestExpiredOrphanKeepsItsRowLiveUntilItsCarveIsReleased(t *testing.T) {
	for _, broken := range []struct{ table, moved string }{{"coder_mission_events", "moved_events"}, {"coder_sessions", "moved_sessions"}} {
		f, conv := orphanFix(t)
		renameTable(t, f.st, broken.table, broken.moved)
		if _, _, err := f.st.ResetStaleSpawning(5 * time.Minute); err == nil {
			t.Fatalf("%s: a failed pass reported success", broken.table)
		}
		renameTable(t, f.st, broken.moved, broken.table)
		row, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
		if row.State != ChildSpawning {
			t.Fatalf("%s: the row left spawning (%s) although the pass failed: its carve would never be released", broken.table, row.State)
		}
		if got := f.capOf(t, conv); got != 4 {
			t.Fatalf("%s: cap = %v after a failed pass", broken.table, got)
		}
		_, ended, err := f.st.ResetStaleSpawning(5 * time.Minute) // the next pass
		if err != nil || len(ended) != 1 {
			t.Fatalf("%s: retry: %v %v", broken.table, ended, err)
		}
		if got := f.capOf(t, conv); got > 2*minCap {
			t.Fatalf("%s: cap = %v after the retry, want the floor", broken.table, got)
		}
		if c, _ := f.st.Carved("p", f.lead.ConversationID); c > 2*minCap {
			t.Fatalf("%s: the lead still has $%v carved", broken.table, c)
		}
	}
}

func TestEndKeepsTheRowLiveWhenItsCarveCannotBeReleased(t *testing.T) {
	for _, broken := range []struct{ table, moved string }{{"coder_mission_events", "moved_events"}, {"coder_sessions", "moved_sessions"}} {
		f := newMC(t, true)
		if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
			t.Fatal(err)
		}
		child := f.linkedChild(t, 4)
		renameTable(t, f.st, broken.table, broken.moved)
		if ok, err := f.st.End("p", child, EndDone); err == nil || ok {
			t.Fatalf("%s: End hid its failure: %v %v", broken.table, ok, err)
		}
		renameTable(t, f.st, broken.moved, broken.table)
		if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildSpawned {
			t.Fatalf("%s: the row ended (%s) although the release failed", broken.table, c.State)
		}
		if ok, err := f.st.End("p", child, EndDone); !ok || err != nil { // retried
			t.Fatalf("%s: retry: %v %v", broken.table, ok, err)
		}
		if got := f.capOf(t, child); got > 2*minCap {
			t.Fatalf("%s: cap = %v after the retry", broken.table, got)
		}
	}
}

// N2: a reservation whose pending conversation belongs to another task does not hold a slot forever.
func TestStaleReservationOfAnotherTasksConversationFailsWithoutStoppingIt(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 0)
	f.mark(t, 0)
	f.mark(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	x := spawn(t, f.st, f.lead)
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], x); err != nil { // the confused task names the same conversation
		t.Fatal(err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(10 * time.Minute) }
	reset, ended, err := f.st.ResetStaleSpawning(5 * time.Minute)
	if err != nil || reset != 0 || len(ended) != 0 {
		t.Fatalf("reset=%d ended=%v err=%v: another task's live conversation must not be reported for stopping", reset, ended, err)
	}
	b, _, _ := f.st.Child("p", f.se.ID, f.ids[1])
	if b.State != ChildFailed || b.EndReason != EndExpired || b.ChildConversationID != "" || b.ReservedUSD != 0 {
		t.Fatalf("the stuck row = %+v, want failed/expired with no conversation and nothing reserved", b)
	}
	if a, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); a.State != ChildSpawned || a.ChildConversationID != x {
		t.Fatalf("the other task's row was touched: %+v", a)
	}
	if c, _ := f.st.ChildCounts("p", f.se.ID); c.Live != 1 {
		t.Fatalf("live = %d, the stuck row still holds a slot", c.Live)
	}
}

// N3: each kind of the lead's rows rotates only within itself.
func TestALeadsFloodOfOneKindEvictsNoOtherKind(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(4)}) // a raise
	f.mark(t, 0)
	who := "lead:" + f.lead.ConversationID
	count := func(kind string) int {
		var n int
		if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_events WHERE session_id = ? AND kind = ?`, f.se.ID, kind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, kind := range []string{"lower", "mode", "budget", "mark", "spawn", "end"} {
		for i := 0; i < 2*MaxMissionEvents; i++ {
			if err := addMissionEvent(f.st.db, "p", f.se.ID, int64(5000+i), kind, who, "x", "n"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count("raise") != 1 {
		t.Fatalf("floods of other kinds evicted the raise: %d", count("raise"))
	}
	for i := 0; i < 2*MaxMissionEvents; i++ {
		_ = addMissionEvent(f.st.db, "p", f.se.ID, 0, "raise", who, "x", "n")
	}
	if count("raise") != MaxMissionEvents {
		t.Fatalf("raise rows = %d, want the newest %d", count("raise"), MaxMissionEvents)
	}
}

func TestBudgetChangesAreRecorded(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, f.scope(), PolicyPatch{DefaultBudgetUSD: fp(1.5)})
	ev, _ := f.st.Events("p", f.se.ID, 0)
	if len(ev) != 1 || ev[0].Kind != "budget" || !strings.Contains(ev[0].Note, "1.50") {
		t.Fatalf("events = %+v", ev)
	}
}

// N4: only identical marks collapse; a change is a row, with the budget in it.
func TestMarkCollapsesOnlyIdenticalMarks(t *testing.T) {
	f := newMC(t, true)
	mk := func(size string, usd float64) {
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{Size: size, BudgetUSD: usd}); err != nil {
			t.Fatal(err)
		}
	}
	mk("", 1)
	mk("", 1)
	mk("", 2)
	mk("large", 2)
	mk("large", 2)
	ev, _ := f.st.Events("p", f.se.ID, 0)
	if len(ev) != 3 {
		t.Fatalf("5 marks, 3 distinct: %d rows %+v", len(ev), ev)
	}
	if !strings.Contains(ev[0].Note, "budget=2.00") || !strings.Contains(ev[0].Note, "size=large") || !strings.Contains(ev[2].Note, "budget=1.00") {
		t.Fatalf("the rows lose the detail: %+v", ev)
	}
}

// N6: End and MarkGone find a reservation by the conversation its spawn created.
func TestEndMatchesAReservationByItsPendingConversation(t *testing.T) {
	f, conv := orphanFix(t)
	if ok, err := f.st.End("p", conv, EndStopped); !ok || err != nil {
		t.Fatalf("end by pending conversation: %v %v", ok, err)
	}
	row, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if row.State != ChildEnded || row.EndReason != EndStopped {
		t.Fatalf("row = %+v", row)
	}
	if got := f.capOf(t, conv); got > 2*minCap {
		t.Fatalf("the orphan keeps its cap: %v", got)
	}
}

// N7: the operator's write is not starved by a lead that keeps changing the policy.
func TestOperatorPolicyWriteOutlastsABusyLead(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	bumps := 0
	f.st.afterPolicyRead = func() {
		if bumps < 20 { // someone else writes between every read and write, 20 times
			bumps++
			if _, err := f.st.db.Exec(`UPDATE coder_mission_policy SET rev = rev + 1`); err != nil {
				t.Error(err)
			}
		}
	}
	var leadErr, opErr error
	boundedly(t, 30*time.Second, func() {
		_, leadErr = f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(3)})
		_, opErr = f.st.SetPolicy(OperatorScope{}, "p", f.se.ID, PolicyPatch{Mode: sp(ModeOff)})
	})
	if !errors.Is(leadErr, ErrInvalid) || !strings.Contains(leadErr.Error(), "try again") {
		t.Fatalf("the lead's starved write: %v, want a retryable error after a bounded number of tries", leadErr)
	}
	if opErr != nil {
		t.Fatalf("the operator's write lost to the churn: %v", opErr)
	}
	if p, _ := f.st.GetPolicy("p", f.se.ID); p.Mode != ModeOff {
		t.Fatalf("mode = %s", p.Mode)
	}
}

// N8 and the terms of the reservation and link statements.
func TestReservationStatementTerms(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 3)
	for i := range f.ids {
		f.mark(t, i)
	}
	lead, now := f.lead.ConversationID, f.st.stamp()
	try := func(i int) bool {
		won, err := takeReservation(f.st.db, "p", f.se.ID, f.ids[i], lead, 9, 0, -1, now)
		if err != nil {
			t.Fatal(err)
		}
		return won
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.st.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE coder_mission_children SET attempts = ? WHERE task_id = ?`, MaxChildAttempts, f.ids[0])
	if try(0) {
		t.Error("the statement ignored the attempts limit")
	}
	exec(`UPDATE coder_mission_children SET state = 'ended' WHERE task_id = ?`, f.ids[1])
	if try(1) {
		t.Error("the statement reserved a row that is not marked")
	}
	exec(`UPDATE coder_mission_children SET lead_conversation_id = 'someone-else' WHERE task_id = ?`, f.ids[2])
	if try(2) {
		t.Error("the statement reserved another lead's row")
	}
	// mode off and a mission that is not running stop it too
	exec(`UPDATE coder_mission_children SET lead_conversation_id = ? WHERE task_id = ?`, lead, f.ids[2])
	f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeOff)})
	if try(2) {
		t.Error("the statement reserved with mode off")
	}
	f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeMarked)})
	exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, f.se.ID)
	if try(2) {
		t.Error("the statement reserved in a mission that is not running")
	}
	exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, f.se.ID)
	if !try(2) {
		t.Error("the statement refused a reservation with every term met")
	}
}

func TestLinkAndTouchStatementsNeedTheRowsLead(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET lead_conversation_id = 'someone-else' WHERE task_id = ?`, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	child := spawn(t, f.st, f.lead)
	if err := f.st.Touch(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrInvalid) {
		t.Errorf("touch of another lead's row: %v", err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], child); !errors.Is(err, ErrInvalid) {
		t.Errorf("pending conversation on another lead's row: %v", err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], child); !errors.Is(err, ErrInvalid) {
		t.Errorf("link of another lead's row: %v", err)
	}
}
