package coderhost

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// F2: a reserved child has no session yet, so the share must count the reservation itself.
func TestTwoReservesBeforeAnySpawnCannotExceedTheShare(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil { // 50% share = $5
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[i], MarkOptions{BudgetUSD: 3}); err != nil {
			t.Fatal(err)
		}
	}
	c, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil || c.State != ChildSpawning {
		t.Fatalf("first reserve: %+v %v", c, err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrLimit) {
		t.Fatalf("a second reserve before the first spawn passed the share ($3 + $3 of $5): %v", err)
	}
	if r, _ := f.st.RoomFor("p", f.se.ID, 3); r.OK {
		t.Fatalf("RoomFor does not count the reserved carve: %+v", r)
	}
	if r, _ := f.st.RoomFor("p", f.se.ID, 2); !r.OK {
		t.Fatalf("RoomFor refused what fits: %+v", r)
	}
}

// The statement holds the share on its own, whatever the room check saw.
func TestReservationStatementHoldsTheShare(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 1)
	f.mark(t, 0)
	f.mark(t, 1)
	f.mark(t, 2)
	now := f.st.stamp()
	lead := f.lead.ConversationID
	if won, err := takeReservation(f.st.db, "p", f.se.ID, f.ids[0], lead, 5, 3, 5, now); err != nil || !won {
		t.Fatalf("first: %v %v", won, err)
	}
	if won, _ := takeReservation(f.st.db, "p", f.se.ID, f.ids[1], lead, 5, 3, 5, now); won {
		t.Fatal("the statement let $3 + $3 through a cap of $5")
	}
	if won, _ := takeReservation(f.st.db, "p", f.se.ID, f.ids[1], lead, 5, 2, 5, now); !won {
		t.Fatal("the statement refused $3 + $2 under a cap of $5")
	}
	if won, _ := takeReservation(f.st.db, "p", f.se.ID, f.ids[2], "someone-else", 5, 1, -1, now); won {
		t.Fatal("the statement reserved a task of another lead")
	}
	if won, _ := takeReservation(f.st.db, "p", f.se.ID, f.ids[2], lead, 5, 1000, -1, now); !won {
		t.Fatal("the share applied to a lead with no cap")
	}
}

// F3: only a checked, unbound scope passes; the tool layer cannot forget the check.
func TestLeadScopeFailsClosedWithoutTheBindingCheck(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.Mark(LeadScope{Caller: f.lead}, "p", f.se.ID, f.ids[0], MarkOptions{}); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a scope built without the binding check marked: %v", err)
	}
	if _, err := f.st.Mark(NewLeadScope(f.lead, BindingNone), "p", f.se.ID, f.ids[0], MarkOptions{}); err != nil {
		t.Fatal(err)
	}
}

// F4: a loop of marks cannot push the policy history out, and one task's repeated marks are one row.
func TestMarkLoopCannotEvictPolicyHistory(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(4)})
	for i := 0; i < 60; i++ {
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(kind string) int {
		n := 0
		ev, _ := f.st.Events("p", f.se.ID, 0)
		for _, e := range ev {
			if e.Kind == kind {
				n++
			}
		}
		return n
	}
	if n := count("mark"); n != 1 {
		t.Fatalf("60 marks of one task left %d rows, want 1", n)
	}
	// rows of many tasks, far past the cap, still leave the raise and the operator's mode row
	for i := 0; i < 3*MaxMissionEvents; i++ {
		if err := addMissionEvent(f.st.db, "p", f.se.ID, int64(1000+i), "mark", "lead:"+f.lead.ConversationID, "x", "n"); err != nil {
			t.Fatal(err)
		}
	}
	if count("raise") != 1 || count("mode") != 1 {
		t.Fatalf("a flood of marks evicted policy rows: raise=%d mode=%d", count("raise"), count("mode"))
	}
	if n := count("mark"); n != MaxMissionEvents {
		t.Fatalf("mark rows = %d, want the newest %d", n, MaxMissionEvents)
	}
	// a flood of the lead's own policy changes evicts neither the operator's rows nor its marks
	for i := 0; i < 3*MaxMissionEvents; i++ {
		if err := addMissionEvent(f.st.db, "p", f.se.ID, 0, "lower", "lead:"+f.lead.ConversationID, "x", "n"); err != nil {
			t.Fatal(err)
		}
	}
	if count("mode") != 1 || count("mark") != MaxMissionEvents {
		t.Fatalf("policy flood evicted: mode=%d mark=%d", count("mode"), count("mark"))
	}
}

func breakEvents(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.db.Exec(`ALTER TABLE coder_mission_events RENAME TO moved_events`); err != nil {
		t.Fatal(err)
	}
}

func TestEventWriteFailuresAreNotDiscarded(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	breakEvents(t, f.st)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err == nil {
		t.Fatal("Reserve hid a failed audit write")
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildMarked {
		t.Fatalf("the reservation stayed although its audit row failed: %+v", c)
	}
}

func TestEndReportsAFailedAuditWriteAndKeepsTheRowLive(t *testing.T) {
	f := newMC(t, true)
	child := f.linkedChild(t, 0)
	breakEvents(t, f.st)
	if ok, err := f.st.End("p", child, EndDone); err == nil || ok {
		t.Fatalf("End hid a failed audit write: %v %v", ok, err)
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildSpawned {
		t.Fatalf("the row ended although its audit row failed: %+v", c)
	}
}

// F5: a decision made from a stale read is not written back.
func TestSetPolicyDoesNotWriteBackAStaleCopy(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	fired := false
	f.st.afterPolicyRead = func() {
		if fired {
			return
		}
		fired = true
		// the operator switches the mode off between the lead's read and its write
		if _, err := f.st.SetPolicy(OperatorScope{}, "p", f.se.ID, PolicyPatch{Mode: sp(ModeOff)}); err != nil {
			t.Error(err)
		}
	}
	p, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(3)})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.GetPolicy("p", f.se.ID)
	if got.Mode != ModeOff || p.Mode != ModeOff || got.MaxChildren != 3 {
		t.Fatalf("the lead's write put a stale mode back: stored %+v returned %+v", got, p)
	}
}

func TestConcurrentPolicyWritesLoseNoUpdate(t *testing.T) {
	f := newMC(t, true)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_, _ = f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(1 + i%4)})
		}(i)
		go func() {
			defer wg.Done()
			_, _ = f.st.SetPolicy(OperatorScope{}, "p", f.se.ID, PolicyPatch{Mode: sp(ModeOff)})
		}()
	}
	wg.Wait()
	if p, _ := f.st.GetPolicy("p", f.se.ID); p.Mode != ModeOff {
		t.Fatalf("the operator's mode off was lost: %+v", p)
	}
}

// F6: a slow spawn that created its conversation is not handed to a second child.
func TestStaleSpawningWithAPendingConversationIsEndedNotRetried(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 1)
	f.mark(t, 0)
	f.mark(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	made, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], made.ConversationID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], f.lead.ConversationID); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a pending conversation that is not the lead's descendant: %v", err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(10 * time.Minute) }
	reset, ended, err := f.st.ResetStaleSpawning(5 * time.Minute)
	if err != nil || reset != 1 || ended != 1 {
		t.Fatalf("reset=%d ended=%d err=%v", reset, ended, err)
	}
	a, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if a.State != ChildFailed || a.EndReason != EndExpired || a.ChildConversationID != made.ConversationID {
		t.Fatalf("the row with a pending conversation = %+v, want failed/expired with the conversation attached", a)
	}
	if b, _, _ := f.st.Child("p", f.se.ID, f.ids[1]); b.State != ChildMarked {
		t.Fatalf("the row with none = %+v, want marked", b)
	}
	// it cannot be linked afterwards
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], made.ConversationID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("link after expiry: %v", err)
	}
}

func TestTouchKeepsASlowSpawnFromGoingStale(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	f.st.now = func() time.Time { return base.Add(4 * time.Minute) }
	if err := f.st.Touch(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	f.st.now = func() time.Time { return base.Add(8 * time.Minute) } // 4 minutes after the touch
	if r, e, _ := f.st.ResetStaleSpawning(5 * time.Minute); r != 0 || e != 0 {
		t.Fatalf("a touched spawn was reset: %d %d", r, e)
	}
	f.st.now = func() time.Time { return base.Add(20 * time.Minute) }
	if r, _, _ := f.st.ResetStaleSpawning(5 * time.Minute); r != 1 {
		t.Fatalf("an untouched spawn was not reset: %d", r)
	}
	if err := f.st.Touch(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("touch of a row that is not spawning: %v", err)
	}
}

// F7: a database error is not reported as a link conflict.
func TestSetSpawnedTellsAConflictFromADatabaseError(t *testing.T) {
	f := newMC(t, true)
	f.addTasks(t, 1)
	f.mark(t, 0)
	f.mark(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	a := spawn(t, f.st, f.lead)
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], a); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[1], a)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "linked to a task already") {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := f.st.db.Exec(`ALTER TABLE coder_mission_children RENAME TO moved_away`); err != nil {
		t.Fatal(err)
	}
	b := spawn(t, f.st, f.lead)
	_, err = f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[1], b)
	if err == nil || strings.Contains(err.Error(), "linked to a task already") {
		t.Fatalf("a database error was reported as a link conflict: %v", err)
	}
}
