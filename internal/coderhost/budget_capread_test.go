package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every cap decision for one turn or one carve reads ONE snapshot of the budget rows, so a Reserve, a Settle or a
// carve that commits in between cannot make two of its reads disagree (an overspend, or a refusal for room that
// was there).

// flipLead moves the lead and its child between two consistent states in one statement: A is (budget 20, child
// carve 15), B is (budget 10, child carve 0).
func flipLead(db *sql.DB, lead, child string, toB bool) error {
	b, c := 20.0, 15.0
	if toB {
		b, c = 10, 0
	}
	_, err := db.Exec(`UPDATE coder_sessions SET budget_usd = CASE conversation_id WHEN ? THEN ? ELSE ? END WHERE conversation_id IN (?, ?)`,
		lead, b, c, lead, child)
	return err
}

func leadWithChild(t *testing.T) (*Store, string, string) {
	t.Helper()
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	if err := st.SetBudget("p", m.ConversationID, 20); err != nil {
		t.Fatal(err)
	}
	child := spawnRaw(t, st, m)
	if err := flipLead(st.db, m.ConversationID, child, false); err != nil {
		t.Fatal(err)
	}
	return st, m.ConversationID, child
}

// A change that commits between TurnCap's reads (here: while it reads the spend) is seen by all of them or by
// none. Reading the budget before and the carve after the change gave 20-0 (an overspend of what was really
// left, 5 or 10) from A then B, and 10-15 (a refusal) from B then A.
func TestTurnCapReadsBudgetSpendAndCarveFromOneSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		startB     bool
		want       float64
		wantUsedUp bool
	}{
		{"from A to B: the state it started in is the one it answers", false, 5, false},
		{"from B to A: the state it started in is the one it answers", true, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, lead, child := leadWithChild(t)
			if err := flipLead(st.db, lead, child, tc.startB); err != nil {
				t.Fatal(err)
			}
			var fired atomic.Int32
			st.onSpent = func(querier) {
				if fired.Add(1) == 1 {
					if err := flipLead(st.db, lead, child, !tc.startB); err != nil {
						t.Error(err)
					}
				}
			}
			capUSD, capped, err := st.TurnCap("p", lead)
			if err != nil || !capped || capUSD != tc.want {
				t.Fatalf("TurnCap = %v, %v, %v: want %v (the answer of the state it started in, not a mix of two)", capUSD, capped, err, tc.want)
			}
		})
	}
}

// The texts of the refusals a cap read gives are part of the contract.
func TestCapReadRefusalTextsAreUnchanged(t *testing.T) {
	st, lead, child := leadWithChild(t)
	if _, err := st.db.Exec(`UPDATE coder_sessions SET budget_usd = 19.9999999 WHERE conversation_id = ?`, child); err != nil {
		t.Fatal(err)
	}
	_, capped, err := st.TurnCap("p", lead)
	want := "the session's budget is used up ($20.00 budget, $0.00 spent, $20.00 given to children)"
	if !capped || !errors.Is(err, errBudgetUsedUp) || err.Error() != want {
		t.Fatalf("TurnCap = %v, %v: want %q", capped, err, want)
	}

	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	why, err := f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 0)
	if err != nil || why != "the mission has a budget, so a child session needs a positive budget of its own (give the task one, or set a default for the mission)" {
		t.Fatalf("no carve: %q, %v", why, err)
	}
	if _, err := f.ts.Reserve(context.Background(), "p", f.se.ID, "owner-1", "w1", 5); err != nil {
		t.Fatal(err)
	}
	why, err = f.st.missionBudgetRefusal(f.st.db, "p", f.se.ID, f.lead.ConversationID, 7)
	if err != nil || why != "a carve of $7.00 would pass the mission's remaining budget ($10.00 left, $5.00 held by child sessions)" {
		t.Fatalf("over the remainder: %q, %v", why, err)
	}
}

// racingDB runs a change once, right after the first read of the budget row of the mission: the moment a Settle or
// a Reserve commits between two of the cap read's statements. It lends a transaction (Begin) as *sql.DB does.
type racingDB struct {
	*sql.DB
	after func()
	done  bool
}

func (r *racingDB) QueryRow(query string, args ...any) *sql.Row {
	row := r.DB.QueryRow(query, args...)
	if !r.done && strings.Contains(query, "budget_usd, spent_usd FROM task_sessions") {
		r.done = true
		r.after()
	}
	return row
}

// A worker's reservation settles (its hold becomes spend) while the host decides a carve: spent and held always
// add to 9 of the mission's 10, so a carve of 5 is refused in every consistent state. Reading the spend before
// the settle and the hold after it saw 0 + 0 and let the carve through.
func TestMissionBudgetRefusalSeesASettleAsOneChange(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	ctx := context.Background()
	r, err := f.ts.Reserve(ctx, "p", f.se.ID, "owner-1", "w1", 9)
	if err != nil {
		t.Fatal(err)
	}
	q := &racingDB{DB: f.st.db, after: func() {
		if err := f.ts.Settle(ctx, "p", r.ID, "owner-1", 9); err != nil {
			t.Error(err)
		}
	}}
	why, err := f.st.missionBudgetRefusal(q, "p", f.se.ID, f.lead.ConversationID, 5)
	if err != nil || why == "" {
		t.Fatalf("a carve of 5 passed while $9 of 10 was held then spent: %q, %v", why, err)
	}
}

// countingTx counts the statements that read the budget row of a session.
type countingTx struct {
	*sql.Tx
	budgetReads int
}

func (c *countingTx) QueryRow(query string, args ...any) *sql.Row {
	if strings.Contains(query, "budget_usd, spent_usd FROM task_sessions") {
		c.budgetReads++
	}
	return c.Tx.QueryRow(query, args...)
}

// The cap read is one query set: the budget row of the session is read once, not once for the refusal and again
// for the admission.
func TestMissionBudgetRefusalReadsTheCommitmentOnce(t *testing.T) {
	f := newHostFix(t)
	f.setMissionBudget(t, 10)
	tx, err := f.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c := &countingTx{Tx: tx}
	if why, err := f.st.missionBudgetRefusal(c, "p", f.se.ID, f.lead.ConversationID, 1); err != nil || why != "" {
		t.Fatalf("a carve of 1 of 10: %q, %v", why, err)
	}
	if c.budgetReads != 1 {
		t.Fatalf("the budget row of the session was read %d times, want 1", c.budgetReads)
	}
}

// A second process flips the lead between its two states for a while; no TurnCap in this process may answer
// anything but one of them (5 or 10).
func TestTurnCapNeverMixesStatesOfAnotherProcess(t *testing.T) {
	st, lead, child := leadWithChild(t)
	var path string
	var seq int
	var name string
	if err := st.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil || path == "" {
		t.Fatalf("database file: %q %v", path, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapReadFlipperProcess$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "CAPREAD_FLIP_DB="+path, "CAPREAD_FLIP_LEAD="+lead, "CAPREAD_FLIP_CHILD="+child)
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	end := time.Now().Add(1500 * time.Millisecond)
	for n := 0; time.Now().Before(end); n++ {
		capUSD, _, err := st.TurnCap("p", lead)
		if err != nil || (capUSD != 5 && capUSD != 10) {
			t.Fatalf("read %d: TurnCap = %v, %v: neither of the two states the other process writes", n, capUSD, err)
		}
	}
}

// TestCapReadFlipperProcess is the other process of the test above; it does nothing when run on its own.
func TestCapReadFlipperProcess(t *testing.T) {
	path := os.Getenv("CAPREAD_FLIP_DB")
	if path == "" {
		t.Skip("helper process")
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(2000)&_pragma=journal_mode(WAL)", path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	end := time.Now().Add(2500 * time.Millisecond)
	for i := 0; time.Now().Before(end); i++ {
		if err := flipLead(db, os.Getenv("CAPREAD_FLIP_LEAD"), os.Getenv("CAPREAD_FLIP_CHILD"), i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
}
