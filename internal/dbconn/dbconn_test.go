package dbconn

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

func abandon(t *testing.T, rollbackErr error, inTx bool) int32 {
	t.Helper()
	db, trip := dbconntest.Open(t)
	trip.RollbackErr = rollbackErr
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inTx {
		if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
	}
	AbandonBegin(conn)
	conn.Close()
	trip.RollbackErr = nil
	c2, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
	return trip.Opened.Load()
}

// No open transaction: the ROLLBACK's refusal is harmless and the connection goes back to the pool.
func TestAbandonBeginKeepsAConnectionThatWasNotInATransaction(t *testing.T) {
	if n := abandon(t, nil, false); n != 1 {
		t.Fatalf("connections opened = %d, want the one reused", n)
	}
}

func TestAbandonBeginKeepsAConnectionThatSaysNoTransactionIsActive(t *testing.T) {
	if n := abandon(t, errors.New("SQL logic error: cannot rollback - no transaction is active (1)"), false); n != 1 {
		t.Fatalf("connections opened = %d, want the one reused", n)
	}
}

// A ROLLBACK that fails for any other reason leaves the connection in an unknown state: it is dropped.
func TestAbandonBeginDropsAConnectionWhoseRollbackFails(t *testing.T) {
	if n := abandon(t, errors.New("disk I/O error"), true); n != 2 {
		t.Fatalf("connections opened = %d, want a fresh one after the drop", n)
	}
}

// A ROLLBACK that never finishes is cut off after the bound, and its connection is dropped; the caller is not
// held (the guard fails the test instead of hanging it).
func TestAbandonBeginBoundsARollbackThatNeverReturns(t *testing.T) {
	db, trip := dbconntest.Open(t)
	trip.RollbackHangs = true
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { AbandonBegin(conn); close(done) }()
	select {
	case <-done:
	case <-time.After(rollbackTimeout + 20*time.Second):
		t.Fatal("AbandonBegin did not return after the bound")
	}
	conn.Close()
	trip.RollbackHangs = false
	c2, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
	if n := trip.Opened.Load(); n != 2 {
		t.Fatalf("connections opened = %d, want a fresh one after the drop", n)
	}
}

// A BEGIN that took effect but is reported as cancelled must not leave the pooled connection inside the
// transaction, and fn never runs.
func TestWithWriteTxCancelledBeginLeavesTheConnectionClean(t *testing.T) {
	db, trip := dbconntest.Open(t)
	trip.Arm()
	ran := false
	err := WithWriteTx(context.Background(), db, func(*sql.Conn) error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if ran {
		t.Fatal("fn ran after a failed BEGIN")
	}
	dbconntest.RequireClean(t, db)
}

// fn's error rolls back, a nil commits, and the connection goes back to the pool outside any transaction either way.
func TestWithWriteTxCommitsOnNilAndRollsBackOnError(t *testing.T) {
	db, _ := dbconntest.Open(t)
	if _, err := db.Exec(`CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	insert := func(conn *sql.Conn) error {
		_, err := conn.ExecContext(context.Background(), `INSERT INTO t VALUES (1)`)
		return err
	}
	boom := errors.New("boom")
	err := WithWriteTx(context.Background(), db, func(c *sql.Conn) error {
		if err := insert(c); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("rows after a failed fn = %d, want 0", n)
	}
	if err := WithWriteTx(context.Background(), db, insert); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("rows after a good fn = %d, want 1", n)
	}
	dbconntest.RequireClean(t, db)
}

// A context that ends inside fn still rolls back (the ROLLBACK does not use it) and releases the write lock.
func TestWithWriteTxRollsBackAfterTheContextEnds(t *testing.T) {
	db, _ := dbconntest.Open(t)
	if _, err := db.Exec(`CREATE TABLE t (v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := WithWriteTx(ctx, db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(context.Background(), `INSERT INTO t VALUES (1)`); err != nil {
			return err
		}
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	dbconntest.RequireClean(t, db)
}
