// Package dbconn holds the one rule about pooled SQLite connections that more than one package needs: what to do
// with a dedicated connection whose BEGIN reported an error.
package dbconn

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"time"
)

// rollbackTimeout bounds the ROLLBACK of AbandonBegin. It is derived from a background context so that a
// cancelled request still rolls back, and it is short because a ROLLBACK takes no lock and finishes at once; one
// that does not is a stuck driver, and the connection is dropped instead of waited for.
const rollbackTimeout = 5 * time.Second

// AbandonBegin makes sure a connection whose BEGIN reported an error goes back to the pool outside any
// transaction. The driver reports the end of the context even for a BEGIN that took effect (a cancellation that
// lands as the statement finishes), and the connection would then be handed to the next caller still inside a
// transaction: "cannot start a transaction within a transaction" (#1315, #1373). Rolling back when none is open
// is a harmless error ("cannot rollback - no transaction is active"), which is ignored. Any other failure of the
// ROLLBACK leaves the connection in an unknown state, so it is dropped from the pool instead of returned. The
// ROLLBACK does not use the caller's context (a cancelled one would skip it) but a bounded one of its own; a
// ROLLBACK that runs out of time is treated like any other failure.
//
// Call it on the error path of a BEGIN, before the connection is closed (conn.Close returns it to the pool).
// It never changes the BEGIN error, which the caller still returns.
func AbandonBegin(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	_, err := conn.ExecContext(ctx, "ROLLBACK")
	if err == nil || strings.Contains(err.Error(), "no transaction is active") {
		return
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// WithWriteTx runs fn inside a BEGIN IMMEDIATE transaction on a dedicated connection: the write lock is taken
// before fn's first read, so a writer that commits in between cannot make a read-then-write fail at once with
// SQLITE_BUSY_SNAPSHOT (517), which busy_timeout does not wait for; the BEGIN waits for the lock instead. A nil
// result from fn commits; any error rolls back. The ROLLBACK does not use ctx (a cancelled one would skip it and
// leave the write lock held), and a failed BEGIN goes through AbandonBegin. The connection is always released.
func WithWriteTx(ctx context.Context, db *sql.DB, fn func(conn *sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		AbandonBegin(conn)
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
