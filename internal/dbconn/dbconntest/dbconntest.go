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
	// RollbackHangs, when set before use, makes every ROLLBACK block until its context ends.
	RollbackHangs bool
	// CommitErr, when set before use, is what every COMMIT returns instead of running: nothing is committed and the
	// transaction stays open, as after a COMMIT the engine refused (the caller's rollback ends it). It covers the
	// COMMIT statement of a dedicated connection and the Commit of a database/sql transaction.
	CommitErr error
	// CommitErrAfter, when set before use, makes every COMMIT run and land, and then return this error: the outcome a
	// caller cannot tell from the error alone (a context that ends as the statement finishes, a connection that
	// breaks after the write). CommitErr takes precedence.
	CommitErrAfter error
	// CommitReportsContext makes a COMMIT statement report the end of the context it was given after it ran and
	// landed, as modernc does for a context that ends as the statement finishes.
	CommitReportsContext bool
	// BeforeExec, when set before use, runs before every statement the driver executes outside BEGIN, COMMIT and
	// ROLLBACK, on the connection that is about to run it: the place for a competing writer to commit between a
	// transaction's read and its write.
	BeforeExec func(query string)
	// BeforePrepare, when set before use, runs before every statement the driver prepares (a prepared statement, or a
	// query: database/sql prepares those), on the connection that is about to prepare it. Statements run through a
	// prepared statement never reach BeforeExec, so this is the place for a competing writer where code prepares
	// its writes.
	BeforePrepare func(query string)
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

// Begin wraps the transaction a database/sql Tx begins, so that its Commit goes through the same hooks as a COMMIT
// statement on a dedicated connection.
func (c *conn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &txn{Tx: tx, trip: c.trip}, nil
}

type txn struct {
	driver.Tx
	trip *Trip
}

func (x *txn) Commit() error {
	if x.trip.CommitErr != nil {
		return x.trip.CommitErr
	}
	err := x.Tx.Commit()
	if err == nil && x.trip.CommitErrAfter != nil {
		return x.trip.CommitErrAfter
	}
	return err
}

func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.trip.BeforePrepare != nil {
		c.trip.BeforePrepare(query)
	}
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return c.Conn.Prepare(query)
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.trip.CommitErr != nil && strings.HasPrefix(query, "COMMIT") {
		return nil, c.trip.CommitErr
	}
	if c.trip.RollbackErr != nil && strings.HasPrefix(query, "ROLLBACK") {
		return nil, c.trip.RollbackErr
	}
	if c.trip.RollbackHangs && strings.HasPrefix(query, "ROLLBACK") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.trip.BeforeExec != nil && !isTxControl(query) {
		c.trip.BeforeExec(query)
	}
	res, err := c.Conn.(driver.ExecerContext).ExecContext(context.WithoutCancel(ctx), query, args)
	if err == nil && c.trip.CommitErrAfter != nil && strings.HasPrefix(query, "COMMIT") {
		return nil, c.trip.CommitErrAfter
	}
	if err == nil && c.trip.CommitReportsContext && strings.HasPrefix(query, "COMMIT") && ctx.Err() != nil {
		return nil, ctx.Err()
	}
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

func isTxControl(q string) bool {
	return strings.HasPrefix(q, "BEGIN") || strings.HasPrefix(q, "COMMIT") || strings.HasPrefix(q, "ROLLBACK")
}

// OpenPath is a pool over such a driver on the database file at path (made and migrated by the caller), with the
// production options (busy timeout, foreign keys) and the default pool size; it is closed with the test.
func OpenPath(t testing.TB, path string) (*sql.DB, *Trip) {
	t.Helper()
	trip := &Trip{}
	db := sql.OpenDB(connector{dsn: "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", trip: trip})
	t.Cleanup(func() { db.Close() })
	return db, trip
}

// CompetingWriter returns a function that commits one write on its own connection that does not wait (busy_timeout
// 0), and says whether it was refused. Where the transaction under test holds no write lock yet it commits (and
// the transaction's own write then fails with SQLITE_BUSY_SNAPSHOT); where it holds the lock it is refused at once.
func CompetingWriter(t testing.TB, path string) func() (refused bool) {
	t.Helper()
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	return func() bool {
		_, err := other.Exec(`CREATE TABLE IF NOT EXISTS zz_competing (x INTEGER); INSERT INTO zz_competing VALUES (1)`)
		return err != nil
	}
}
