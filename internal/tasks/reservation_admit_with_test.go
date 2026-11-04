package tasks

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
)

// countingDB counts the statements that contain match.
type countingDB struct {
	*sql.DB
	match string
	n     atomic.Int32
}

func (c *countingDB) count(q string) {
	if strings.Contains(q, c.match) {
		c.n.Add(1)
	}
}

func (c *countingDB) QueryContext(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	c.count(q)
	return c.DB.QueryContext(ctx, q, a...)
}

func (c *countingDB) QueryRowContext(ctx context.Context, q string, a ...any) *sql.Row {
	c.count(q)
	return c.DB.QueryRowContext(ctx, q, a...)
}

// AdmitHoldWith and AdmitRaiseWith decide on the commitment they are handed, not on one read again: a caller that
// read it in its snapshot must get the answer of that snapshot. The rows say the session has room (budget 100,
// nothing held); the commitment handed in says it has none.
func TestAdmitWithDecidesOnTheCommitmentItIsGiven(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 100)
	given := Commitment{Budget: 10, Held: 9.5}
	for name, admit := range map[string]func() (string, error){
		"hold":  func() (string, error) { return AdmitHoldWith(bg, db, "default", id, 1, given) },
		"raise": func() (string, error) { return AdmitRaiseWith(bg, db, "default", id, "no-such-child", 1, given) },
	} {
		why, err := admit()
		if err != nil || !strings.Contains(why, "has $0.5000 left of $10.00 (spent $0.0000, held $9.5000") {
			t.Fatalf("%s on a commitment with $0.50 left: %q, %v", name, why, err)
		}
	}
	// and a commitment with room admits it although the rows' own budget is nearly used
	if _, err := db.Exec(`UPDATE task_sessions SET budget_usd = 0.5 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	roomy := Commitment{Budget: 100}
	if why, err := AdmitRaiseWith(bg, db, "default", id, "no-such-child", 1, roomy); err != nil || why != "" {
		t.Fatalf("raise on a commitment with room: %q, %v", why, err)
	}
}

// The raise path reads the commitment once: AdmitRaise reads it (one read of the session's budget row) and
// AdmitRaiseWith, handed it, reads none.
func TestAdmitRaiseWithDoesNotReadTheCommitmentAgain(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 100)
	c := &countingDB{DB: db, match: "budget_usd, spent_usd FROM task_sessions"}
	com, err := ReadCommitment(bg, c, "default", id)
	if err != nil || c.n.Swap(0) != 1 {
		t.Fatalf("reading a commitment: %v", err)
	}
	if _, err := AdmitRaiseWith(bg, c, "default", id, "no-such-child", 1, com); err != nil {
		t.Fatal(err)
	}
	if n := c.n.Swap(0); n != 0 {
		t.Fatalf("AdmitRaiseWith read the session's budget row %d times, want 0", n)
	}
	if _, err := AdmitRaise(bg, c, "default", id, "no-such-child", 1); err != nil {
		t.Fatal(err)
	}
	if n := c.n.Load(); n != 1 {
		t.Fatalf("AdmitRaise read the session's budget row %d times, want 1", n)
	}
}
