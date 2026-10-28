package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
)

// The person's restrictions bind the lead: an off stays off, and a cap is the most the lead may raise to.

func TestOperatorOffStaysOffAgainstTheLead(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeOff)})
	before, _ := f.st.GetPolicy("p", f.se.ID)
	if !before.OperatorModeOff {
		t.Fatalf("the operator's off was not recorded as a floor: %+v", before)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeMarked)}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("the lead left the operator's off: %v", err)
	}
	// one call with all three fields cannot sneak past, and writes nothing
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeMarked), SharePct: ip(80), MaxChildren: ip(4)}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("the lead's combined raise got through: %v", err)
	}
	after, _ := f.st.GetPolicy("p", f.se.ID)
	if after.Mode != ModeOff || after.Rev != before.Rev || after.SharePct != before.SharePct || after.MaxChildren != before.MaxChildren {
		t.Fatalf("a refused write changed the policy: %+v -> %+v", before, after)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a mark under the operator's off: %v", err)
	}
	// staying off is not a raise
	f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeOff)})
	// the operator lifts the floor: the lead has its freedom back
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeMarked)})
	if p := f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeOff)}); p.OperatorModeOff {
		t.Fatalf("the lead's own off became the operator's: %+v", p)
	}
	if p := f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeMarked)}); p.Mode != ModeMarked {
		t.Fatalf("the lead could not switch back after the operator lifted its off: %+v", p)
	}
	// an operator off set while the mode already is off still holds
	f.setPolicy(t, f.scope(), PolicyPatch{Mode: sp(ModeOff)})
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeOff)})
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeMarked)}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an operator off written over the lead's off did not hold: %v", err)
	}
}

func TestOperatorCapsAreNotExceededByTheLead(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{MaxChildren: ip(1), SharePct: ip(20)})
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(2)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("the lead passed the operator's max_children cap: %v", err)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{SharePct: ip(21)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("the lead passed the operator's share cap: %v", err)
	}
	before, _ := f.st.GetPolicy("p", f.se.ID)
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{Mode: sp(ModeMarked), SharePct: ip(80), MaxChildren: ip(4)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("one call with all three fields got past the caps: %v", err)
	}
	if after, _ := f.st.GetPolicy("p", f.se.ID); after.Rev != before.Rev || after.SharePct != 20 || after.MaxChildren != 1 {
		t.Fatalf("a refused write changed the policy: %+v", after)
	}
	// below the cap the lead is still free: lower, then raise back up to the cap and no further
	f.setPolicy(t, OperatorScope{}, PolicyPatch{MaxChildren: ip(3), SharePct: ip(40)})
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(1), SharePct: ip(10)})
	if p := f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(3), SharePct: ip(40)}); p.MaxChildren != 3 || p.SharePct != 40 {
		t.Fatalf("the lead could not raise to the operator's cap: %+v", p)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{MaxChildren: ip(4)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("raise past the cap of 3: %v", err)
	}
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{SharePct: ip(41)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("raise past the cap of 40: %v", err)
	}
	// the lead cannot lift a cap, and a number with its own cap cleared is ambiguous
	if _, err := f.st.SetPolicy(f.scope(), "p", f.se.ID, PolicyPatch{ClearMaxChildrenCap: true}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("the lead cleared a cap: %v", err)
	}
	if _, err := f.st.SetPolicy(OperatorScope{}, "p", f.se.ID, PolicyPatch{MaxChildren: ip(2), ClearMaxChildrenCap: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a number and its cap's clearing together: %v", err)
	}
	// the operator lifts the caps: the lead's freedom is back, up to the session setting and the hard maximum
	if p := f.setPolicy(t, OperatorScope{}, PolicyPatch{ClearMaxChildrenCap: true, ClearSharePctCap: true}); p.OperatorMaxChildren != nil || p.OperatorSharePct != nil {
		t.Fatalf("caps not cleared: %+v", p)
	}
	if p := f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(4), SharePct: ip(ShareHardMax)}); p.MaxChildren != 4 || p.SharePct != ShareHardMax {
		t.Fatalf("the lead could not use its freedom after the caps were lifted: %+v", p)
	}
}

func TestOperatorFloorChangesAreAuditedAsTheOperators(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeOff), MaxChildren: ip(1)})
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeMarked), ClearMaxChildrenCap: true})
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(3)})
	ev, _ := f.st.Events("p", f.se.ID, 50)
	floors, leadRaise := 0, 0
	for _, e := range ev {
		switch {
		case e.Kind == "floor" && e.Who == "operator":
			floors++
		case e.Kind == "floor":
			t.Errorf("a floor row not by the operator: %+v", e)
		case e.Kind == "raise" && e.Who == "lead:"+f.lead.ConversationID:
			leadRaise++
		}
	}
	if floors < 4 || leadRaise != 1 {
		t.Fatalf("floors=%d leadRaise=%d in %+v", floors, leadRaise, ev)
	}
}

// 2: an empty id matches nothing.
func TestEndWithAnEmptyConversationEndsNothing(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	for _, end := range []func() (bool, error){
		func() (bool, error) { return f.st.End("p", "", EndStopped) },
		func() (bool, error) { return f.st.MarkGone("p", "") },
	} {
		if ok, err := end(); ok || err != nil {
			t.Fatalf("an empty id ended something: %v %v", ok, err)
		}
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildSpawning {
		t.Fatalf("an unrelated reservation was ended: %+v", c)
	}
}

// 3: a conversation is pending on one task only.
func TestAConversationIsPendingOnOneTaskOnly(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	f.mark(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	x := spawn(t, f.st, f.lead)
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil { // the same task again is a refresh
		t.Fatalf("noting the same pending twice: %v", err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], x); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second task took a pending conversation: %v", err)
	}
	// and the schema holds it too
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET pending_conversation_id = ? WHERE task_id = ?`, x, f.ids[1]); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("the schema let two rows share a pending conversation: %v", err)
	}
	// a conversation another task already runs in is refused too
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], x); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a task took a conversation another task runs in as its pending: %v", err)
	}
}

// 4: the limits a reservation holds are the ones read when it takes the write lock, not the ones from before.
func TestReserveHoldsTheCeilingLoweredBeforeItsTransaction(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	f.mark(t, 1)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(2)})
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	f.st.beforeReserveTx = func() { f.setPolicy(t, OperatorScope{}, PolicyPatch{MaxChildren: ip(1)}) }
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); !errors.Is(err, ErrLimit) {
		t.Fatalf("a reservation passed the ceiling the operator had just lowered: %v", err)
	}
	if c, _ := f.st.ChildCounts("p", f.se.ID); c.Live != 1 {
		t.Fatalf("live = %d", c.Live)
	}
}

func TestReserveHoldsTheShareLoweredBeforeItsTransaction(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 3}); err != nil { // 50% of 10 allows 3
		t.Fatal(err)
	}
	f.st.beforeReserveTx = func() { f.setPolicy(t, OperatorScope{}, PolicyPatch{SharePct: ip(20)}) } // allows 2
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrLimit) {
		t.Fatalf("a reservation passed the share the operator had just lowered: %v", err)
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildMarked || c.ReservedUSD != 0 {
		t.Fatalf("row = %+v", c)
	}
}

func TestAReservationThatLosesLeavesNoRuleRow(t *testing.T) {
	f := newMC(t, true)
	f.setPolicy(t, OperatorScope{}, PolicyPatch{Mode: sp(ModeAll)})
	// the mission stops running between the checks and the reserving statement: the statement loses
	f.st.beforeReserveTx = func() {
		if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'paused' WHERE id = ?`, f.se.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); !errors.Is(err, ErrLimit) {
		t.Fatalf("the reservation of a stopped mission: %v", err)
	}
	if n := rowsOf(t, f.st, `SELECT * FROM coder_mission_children`); n != "" {
		t.Fatalf("a losing reservation left a rule row behind: %s", n)
	}
}

// 6c: an ended spawning row holds no carve.
func TestEndingASpawningRowZeroesItsReservedCarve(t *testing.T) {
	f, conv := orphanFix(t)
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.ReservedUSD != 4 {
		t.Fatalf("setup: reserved = %v", c.ReservedUSD)
	}
	if ok, err := f.st.End("p", conv, EndStopped); !ok || err != nil {
		t.Fatalf("end: %v %v", ok, err)
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[0]); c.State != ChildEnded || c.ReservedUSD != 0 {
		t.Fatalf("the ended row still reserves: %+v", c)
	}
}

// 6b: deleting a conversation that was only pending ends its row.
func TestDeletingAPendingConversationMarksItsRowGone(t *testing.T) {
	f, conv := orphanFix(t)
	a, _ := ai.NewAIStore(f.st.db)
	if err := a.DeleteConversation(conv, "p"); err != nil {
		t.Fatal(err)
	}
	c, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if c.State != ChildGone || c.EndReason != EndDeleted || c.ReservedUSD != 0 {
		t.Fatalf("row after its pending conversation was deleted = %+v", c)
	}
}

// 6a: the host's way to find a carve nothing points at.
func TestFindOrphanCarvesListsOnlyCarvesNoRowPointsAt(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 2}); err != nil { // creates the policy row
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	pointed, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], pointed.ConversationID); err != nil {
		t.Fatal(err)
	}
	orphan, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 3}) // carved, then the process died
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.FindOrphanCarves(time.Minute); len(got) != 0 {
		t.Fatalf("a fresh spawn was listed: %+v", got)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(10 * time.Minute) }
	got, err := f.st.FindOrphanCarves(time.Minute)
	if err != nil || len(got) != 1 || got[0].ConversationID != orphan.ConversationID || got[0].BudgetUSD != 3 || got[0].LeadConversationID != f.lead.ConversationID {
		t.Fatalf("orphans = %+v, %v", got, err)
	}
}

// secondWriter opens another handle on the store's database file with a short busy timeout, to stand for
// another process writing (the operator's, or another lead's).
func secondWriter(t *testing.T, st *Store) *sql.DB {
	t.Helper()
	var seq int
	var name, file string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &file); err != nil || file == "" {
		t.Fatalf("database file: %q %v", file, err)
	}
	db2, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(150)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	return db2
}

// Reserve holds the database's write lock from its first statement until it commits: a writer that comes in
// between its reads and its reserving statement does not get through, and every read goes through the
// transaction (with one connection in the pool, a read through the pool would wait for the transaction forever).
func TestReserveHoldsTheWriteLockWhileItDecides(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	db2 := secondWriter(t, f.st)
	f.st.db.SetMaxOpenConns(1)
	var ran bool
	var through error
	f.st.afterReserveReads = func() {
		ran = true
		_, through = db2.Exec(`UPDATE coder_mission_policy SET max_children = 1 WHERE session_id = ?`, f.se.ID)
	}
	var err error
	boundedly(t, 10*time.Second, func() { _, err = f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]) })
	if err != nil || !ran {
		t.Fatalf("reserve: ran=%v err=%v", ran, err)
	}
	if through == nil {
		t.Fatal("another writer got through between Reserve's reads and its reserving statement")
	}
	if _, err := db2.Exec(`UPDATE coder_mission_policy SET max_children = 1 WHERE session_id = ?`, f.se.ID); err != nil {
		t.Fatalf("the lock was not released at the commit: %v", err)
	}
}

// Reserve sums the lead's spend before it takes the write lock: a long event log does not hold other
// writers up. Every Spent read must come before the lock, and a writer must get through while it runs.
func TestReserveReadsTheSpendBeforeTakingTheWriteLock(t *testing.T) {
	f := newMC(t, true)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	conn, err := f.st.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 200000)
		INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload)
		SELECT 'p', ?, 't1', i, 'now', 1, 'usage.updated', printf('{"agentId":"","costUsd":%f}', i * 0.00001) FROM n`, f.lead.ConversationID); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 1}); err != nil {
		t.Fatal(err)
	}
	db2 := secondWriter(t, f.st)
	reads, inTx := 0, 0
	var blocked error
	f.st.onSpent = func(q querier) {
		reads++
		if _, ok := q.(*sql.Tx); ok {
			inTx++
		}
		if _, err := db2.Exec(`UPDATE coder_mission_policy SET max_children = 2 WHERE session_id = ?`, f.se.ID); err != nil {
			blocked = err
		}
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	if reads == 0 || inTx != 0 || blocked != nil {
		t.Fatalf("spend reads=%d inside the transaction=%d, a writer during them: %v", reads, inTx, blocked)
	}
}
