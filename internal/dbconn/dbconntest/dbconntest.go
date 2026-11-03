// Package dbconntest is a SQLite driver wrapper for tests of the code that begins a transaction on a dedicated
// pooled connection (see dbconn.AbandonBegin).
package dbconntest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	sqlite "modernc.org/sqlite"
)

// Trip makes the driver report a cancelled context for the next BEGIN that goes through: the statement runs
// and is in effect, and the call returns context.Canceled. That is what modernc's statement path does when the
// context ends just as a statement finishes (its conn.exec defers `if done { err = ctx.Err() }` over a step
// that went through). One Arm covers one BEGIN.
type Trip struct {
	armed atomic.Bool
	// RollbackErr, when set before use, is what every ROLLBACK returns instead of running.
	RollbackErr error
	// Opened counts the connections the pool has opened.
	Opened atomic.Int32
}

// Arm makes the next BEGIN report context.Canceled.
func (p *Trip) Arm() { p.armed.Store(true) }

type connector struct {
	dsn  string
	trip *Trip
}

func (c connector) Connect(context.Context) (driver.Conn, error) {
	c.trip.Opened.Add(1)
	inner, err := (&sqlite.Driver{}).Open(c.dsn)
	return &conn{Conn: inner, trip: c.trip}, err
}

func (c connector) Driver() driver.Driver { return &sqlite.Driver{} }

type conn struct {
	driver.Conn
	trip *Trip
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.trip.RollbackErr != nil && strings.HasPrefix(query, "ROLLBACK") {
		return nil, c.trip.RollbackErr
	}
	res, err := c.Conn.(driver.ExecerContext).ExecContext(context.WithoutCancel(ctx), query, args)
	if err == nil && strings.HasPrefix(query, "BEGIN") && c.trip.armed.CompareAndSwap(true, false) {
		return nil, context.Canceled
	}
	return res, err
}

// Open is a one-connection database over such a driver, in a temp file; it is closed with the test.
func Open(t testing.TB) (*sql.DB, *Trip) {
	t.Helper()
	trip := &Trip{}
	db := sql.OpenDB(connector{dsn: filepath.Join(t.TempDir(), "b.db"), trip: trip})
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db, trip
}

// RequireClean fails the test if the pool's only connection is still inside a transaction.
func RequireClean(t testing.TB, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("the pooled connection was returned inside a transaction: %v", err)
	}
	_, _ = c.ExecContext(ctx, "ROLLBACK")
}
