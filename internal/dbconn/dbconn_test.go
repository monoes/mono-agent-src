package dbconn

import (
	"context"
	"errors"
	"testing"

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
