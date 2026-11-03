package tasks

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"log"
	"path/filepath"
	"strings"
	"testing"

	sqlite "modernc.org/sqlite"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// A pooled connection left inside a transaction is reported by the store method that meets it (#1315's
// diagnostic); the call still fails exactly as before.
func TestADirtyPooledConnectionIsNamedInTheLog(t *testing.T) {
	s, db, _ := newTestStore(t)
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	conn.Close() // back in the pool, still inside its transaction

	buf := captureLog(t)
	err = s.tx(context.Background(), func(x dbx) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "tasks: begin") {
		t.Fatalf("a dirty connection must still fail the call as before: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "returned inside an open transaction") || !strings.Contains(got, "TestADirtyPooledConnectionIsNamedInTheLog") {
		t.Fatalf("log = %q", got)
	}
}

func TestACleanBeginLogsNothing(t *testing.T) {
	s, _, _ := newTestStore(t)
	buf := captureLog(t)
	if err := s.tx(context.Background(), func(x dbx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("log = %q", buf.String())
	}
}

// beginThenCancelledConnector is a driver over SQLite that behaves as modernc's statement path does when the
// context ends just as a statement finishes: a BEGIN of a context carrying beginEndsKey runs and is in effect,
// and the call still returns the context's error. (The driver's conn.exec defers `if done { err = ctx.Err() }`
// over a step that went through; its tx.exec avoids this for the Tx path, as its comment says.)
type beginEndsKey struct{}

type beginThenCancelledConnector struct{ dsn string }

func (c beginThenCancelledConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := (&sqlite.Driver{}).Open(c.dsn)
	return &beginThenCancelledConn{inner}, err
}

func (c beginThenCancelledConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type beginThenCancelledConn struct{ driver.Conn }

func (c *beginThenCancelledConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.Conn.(driver.ExecerContext).ExecContext(context.WithoutCancel(ctx), query, args)
	if err == nil && ctx.Value(beginEndsKey{}) != nil && strings.HasPrefix(query, "BEGIN") {
		return nil, context.Canceled
	}
	return res, err
}

// A BEGIN that went through but is reported as cancelled must not leave its connection inside the
// transaction: the next call on the pool would fail with "cannot start a transaction within a
// transaction" (#1315, seen in dynorg when a conductor closed while its watcher began a transaction).
func TestACancelledBeginLeavesNoOpenTransactionOnThePooledConnection(t *testing.T) {
	db := sql.OpenDB(beginThenCancelledConnector{dsn: filepath.Join(t.TempDir(), "b.db")})
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	s := NewStore(db)
	ending := context.WithValue(context.Background(), beginEndsKey{}, true)
	for name, call := range map[string]func(context.Context) error{
		"write": func(ctx context.Context) error { return s.tx(ctx, func(x dbx) error { return nil }) },
		"read":  func(ctx context.Context) error { return s.snapshot(ctx, func(x dbx) error { return nil }) },
	} {
		if err := call(ending); err == nil || !strings.Contains(err.Error(), "tasks: begin") {
			t.Fatalf("%s: the setup should fail the begin: %v", name, err)
		}
		if err := call(context.Background()); err != nil {
			t.Fatalf("%s: the call after a cancelled begin failed: %v", name, err)
		}
	}
}
