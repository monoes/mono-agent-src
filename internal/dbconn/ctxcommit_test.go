package dbconn

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// These tests pin what the repo's transaction code relies on when a commit's context ends while the commit waits for
// a lock. Rollback-journal mode is used because that is where a COMMIT waits for a reader on another connection
// (WAL commits do not wait for readers). The expected answer in every case: the commit returns the context's error
// well before busy_timeout, the row is not applied, and the connection is usable again.

func openJournal(t *testing.T, mode string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.db")
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(" + mode + ")"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db, dsn
}

// holdRead opens a second connection and leaves a read transaction open on it: a SHARED lock that a rollback-journal
// COMMIT must wait for. The returned func releases it.
func holdRead(t *testing.T, db *sql.DB) func() {
	t.Helper()
	rc, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.ExecContext(context.Background(), "BEGIN"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := rc.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return func() {
		_, _ = rc.ExecContext(context.Background(), "ROLLBACK")
		rc.Close()
	}
}

func rowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func cancelSoon(cancel context.CancelFunc) { time.AfterFunc(150*time.Millisecond, cancel) }

// The database/sql pattern (BeginTx(ctx), defer Rollback, Commit): used by internal/doomsday/store, the person-deny
// transaction in internal/coderhost and the agent-access grant in internal/secrets.
func TestTxCommitEndsWithCtxErrorAndRollsBack(t *testing.T) {
	db, _ := openJournal(t, "delete")
	release := holdRead(t, db)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `INSERT INTO t (v) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	cancelSoon(cancel)
	start := time.Now()
	err = tx.Commit()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Commit waited %v: it did not stop at the context", d)
	}
	_ = tx.Rollback() // the deferred rollback of the callers; ErrTxDone is fine
	release()
	if n := rowCount(t, db); n != 0 {
		t.Fatalf("%d rows visible after a commit that returned a context error; the write must not be applied", n)
	}
	// the pooled connection is clean and the next transaction works
	if _, err := db.Exec(`INSERT INTO t (v) VALUES ('y')`); err != nil {
		t.Fatalf("connection not reusable: %v", err)
	}
	if n := rowCount(t, db); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// The same transaction in WAL mode (what the app's database uses): a reader does not hold a COMMIT up, so the commit
// goes through immediately. Documents why the new behaviour is rare in production.
func TestTxCommitWALDoesNotWaitForReaders(t *testing.T) {
	db, _ := openJournal(t, "wal")
	release := holdRead(t, db)
	defer release()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO t (v) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a WAL commit waited for a reader")
	}
}

// The dedicated-connection pattern (Conn, BEGIN IMMEDIATE, work, ExecContext(ctx, "COMMIT"), rollback on failure):
// internal/procbind (MintTx, revoker), internal/vault, internal/secrets, internal/coderhost bindinghost, the settings
// patch (which commits with WithoutCancel). A failed COMMIT leaves the transaction open on the connection, so the
// caller's rollback, and dbconn.AbandonBegin when it is the BEGIN that failed, is what makes the row not applied.
func TestRawCommitCtxErrorThenRollbackLeavesNothing(t *testing.T) {
	db, _ := openJournal(t, "delete")
	release := holdRead(t, db)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO t (v) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	cancelSoon(cancel)
	start := time.Now()
	err = func() error { _, e := conn.ExecContext(ctx, "COMMIT"); return e }()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("COMMIT = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("COMMIT waited %v", d)
	}
	// What the callers' defer does: a ROLLBACK on a fresh context. AbandonBegin is that same step.
	AbandonBegin(conn)
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	release()
	if n := rowCount(t, db); n != 0 {
		t.Fatalf("%d rows visible after a COMMIT that returned a context error", n)
	}
	c2, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if _, err := c2.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("connection came back inside a transaction: %v", err)
	}
	if _, err := c2.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

// internal/tasks.Store.tx commits with WithoutCancel: a context that ends after the work must not fail a commit that
// would go through, and one that cannot get the lock still only waits busy_timeout. Once the reader is gone the
// commit succeeds with a cancelled ctx.
func TestCommitWithoutCancelIgnoresDoneCtx(t *testing.T) {
	db, _ := openJournal(t, "delete")
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO t (v) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		t.Fatalf("COMMIT with WithoutCancel: %v", err)
	}
	if n := rowCount(t, db); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}
