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

// commitTimeout bounds the COMMIT of WithWriteTx. The write lock is already held, so the COMMIT only has to reach the
// disk; one that takes longer than this is a stuck driver.
const commitTimeout = 30 * time.Second

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
// result from fn commits; any error rolls back. Neither the ROLLBACK nor the COMMIT uses ctx (a cancelled one would
// skip the first and leave the write lock held; the second is bounded on its own once fn has succeeded), and both
// a failed BEGIN and the rollback of a failed fn or COMMIT go through AbandonBegin, so a connection whose ROLLBACK fails is dropped instead of returned inside the transaction. The connection is always released.
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
			AbandonBegin(conn)
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	// The COMMIT does not use ctx either: once fn has succeeded, a context that ends as the COMMIT finishes would
	// make the driver report an error for data that is committed (#1460).
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
	defer cancel()
	if _, err := conn.ExecContext(commitCtx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// ConnTx lends the dedicated connection of a WithWriteTx to code written against an open *sql.Tx: its methods run on
// the connection, inside the transaction, with Ctx for the calls that carry none.
type ConnTx struct {
	Ctx  context.Context
	Conn *sql.Conn
}

func (c ConnTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return c.Conn.ExecContext(ctx, q, args...)
}
func (c ConnTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return c.Conn.QueryContext(ctx, q, args...)
}
func (c ConnTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return c.Conn.QueryRowContext(ctx, q, args...)
}
func (c ConnTx) Exec(q string, args ...any) (sql.Result, error) {
	return c.Conn.ExecContext(c.Ctx, q, args...)
}
func (c ConnTx) Query(q string, args ...any) (*sql.Rows, error) {
	return c.Conn.QueryContext(c.Ctx, q, args...)
}
func (c ConnTx) QueryRow(q string, args ...any) *sql.Row {
	return c.Conn.QueryRowContext(c.Ctx, q, args...)
}
