package coderhost

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// beginCounter wraps the sqlite driver and counts the transactions its connections begin.
type beginCounter struct {
	base driver.Driver
	n    *atomic.Int64
}

func (d beginCounter) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return beginCountConn{c, d.n}, nil
}

type beginCountConn struct {
	driver.Conn
	n *atomic.Int64
}

func (c beginCountConn) Begin() (driver.Tx, error) {
	c.n.Add(1)
	return c.Conn.Begin()
}

var (
	registerBeginCounter sync.Once
	beginsCounted        atomic.Int64
)

// countBegins points a second store at the database of st through a driver that counts the transactions begun.
func countBegins(t *testing.T, st *Store) (*Store, *atomic.Int64) {
	t.Helper()
	var seq int
	var name, path string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil || path == "" {
		t.Fatalf("database file: %q %v", path, err)
	}
	registerBeginCounter.Do(func() { sql.Register("sqlite-begin-counting", beginCounter{base: st.db.Driver(), n: &beginsCounted}) })
	db, err := sql.Open("sqlite-begin-counting", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db), &beginsCounted
}

// A session with no budget has nothing to agree with: its cap is answered without a transaction. A session with
// a budget reads in exactly one.
func TestTurnCapOpensATransactionOnlyForASessionWithABudget(t *testing.T) {
	st, lead, _ := leadWithChild(t)
	free, _ := master(t, st, "coding")
	cs, begins := countBegins(t, st)
	begins.Store(0)
	if _, capped, err := cs.TurnCap("p", free.ConversationID); err != nil || capped {
		t.Fatalf("a session without a budget: capped %v, %v", capped, err)
	}
	if n := begins.Load(); n != 0 {
		t.Fatalf("a session without a budget began %d transactions, want 0", n)
	}
	if _, capped, err := cs.TurnCap("p", lead); err != nil || !capped {
		t.Fatalf("a session with a budget: capped %v, %v", capped, err)
	}
	if n := begins.Load(); n != 1 {
		t.Fatalf("a session with a budget began %d transactions, want exactly 1 snapshot", n)
	}
}

// onePool limits st to one connection: a transaction that is not ended keeps it for good.
func onePool(t *testing.T, st *Store) {
	t.Helper()
	st.db.SetMaxOpenConns(1)
	t.Cleanup(func() { st.db.SetMaxOpenConns(0) })
}

// connectionIsFree fails unless the only connection of the pool is free and in no transaction: it takes it
// (bounded, so a leaked transaction fails the test instead of hanging it) and begins a write transaction on it.
func connectionIsFree(t *testing.T, st *Store, when string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := st.db.Conn(ctx)
	if err != nil {
		t.Fatalf("%s: the connection is still held (a transaction was not ended): %v", when, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("%s: the connection is inside a transaction: %v", when, err)
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
}

// inSnapshot ends the transaction it begins on every path: a result, an error and a panic. A leaked read
// transaction pins the log and, on a pool of one connection, hangs everything after it.
func TestInSnapshotEndsItsTransactionOnEveryPath(t *testing.T) {
	st := newStoreT(t)
	onePool(t, st)
	read := func(q querier) (int, error) {
		var n int
		return n, q.QueryRow(`SELECT COUNT(*) FROM coder_sessions`).Scan(&n)
	}
	if _, err := inSnapshot(st.db, read); err != nil {
		t.Fatal(err)
	}
	connectionIsFree(t, st, "after a result")

	boom := fmt.Errorf("boom")
	if _, err := inSnapshot(st.db, func(q querier) (int, error) { _, _ = read(q); return 0, boom }); err != boom {
		t.Fatalf("error not returned: %v", err)
	}
	connectionIsFree(t, st, "after an error")

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not pass through")
			}
		}()
		_, _ = inSnapshot(st.db, func(q querier) (int, error) { _, _ = read(q); panic("boom") })
	}()
	connectionIsFree(t, st, "after a panic")

	// the cap read itself, on a session that does not exist (an error from inside the snapshot)
	if _, _, err := st.TurnCap("p", "no-such-conversation"); err == nil {
		t.Fatal("a missing session is not an error")
	}
	connectionIsFree(t, st, "after TurnCap of a missing session")
}

// A caller that already is in a transaction is not given another: inSnapshot reads on it and leaves it open for
// its caller to end.
func TestInSnapshotLeavesACallersTransactionOpen(t *testing.T) {
	st := newStoreT(t)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := inSnapshot(tx, func(q querier) (int, error) {
		if q != querier(tx) {
			t.Error("fn was not given the caller's transaction")
		}
		return 0, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT 1`); err != nil {
		t.Fatalf("the caller's transaction was ended: %v", err)
	}
}

// The raise of a cap (the BossGrantBudget pre-check passes the database) reads the holder's budget, carve and
// spend in one snapshot: a spend that lands between them is not seen by the decision. The holder has $20, its
// children hold $15, a raise of $3 fits; a spend of $19 that commits while the limits are read would make the
// holder's room $1 and refuse it, a state the read did not start in.
func TestBudgetRaiseRefusalReadsOneSnapshot(t *testing.T) {
	st, lead, child := leadWithChild(t)
	b := BudgetRequest{ProfileID: "p", Target: TargetLead, TargetConversationID: child}
	spend := func() {
		addTurn(t, st, lead, "t1", "completed", "x", time.Now().Add(-time.Hour))
		event(t, st, lead, "t1", 1, "usage.updated", `{"agentId":"","costUsd":19}`)
	}
	var fired atomic.Int32
	st.onSpent = func(querier) {
		if fired.Add(1) == 1 {
			spend()
		}
	}
	why, err := st.budgetRaiseRefusal(st.db, b, 3)
	if err != nil || why != "" {
		t.Fatalf("a raise of $3 that fit when the read began: %q, %v", why, err)
	}
	if fired.Load() == 0 {
		t.Fatal("the spend never landed during the read")
	}
	// the spend is real: a read that starts after it refuses
	if why, err := st.budgetRaiseRefusal(st.db, b, 3); err != nil || why == "" {
		t.Fatalf("a raise after the spend: %q, %v; want a refusal", why, err)
	}
}

// The host's own check of a carve counts the mission's spend: $8 of $10 spent leaves $2, and a carve of $5 is
// refused with the host's text, not (as the admission's own check would) with another reason.
func TestMissionBudgetRefusalCountsTheMissionsSpend(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET spent_usd = 8 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 5)
	want := "a carve of $5.00 would pass the mission's remaining budget ($2.00 left, $0.00 held by child sessions)"
	if err != nil || why != want {
		t.Fatalf("a carve of 5 with 8 of 10 spent: %q, %v; want %q", why, err, want)
	}
	if why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 2); err != nil || why != "" {
		t.Fatalf("a carve of exactly what is left: %q, %v", why, err)
	}
}
