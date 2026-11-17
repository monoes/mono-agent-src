package commsync

import (
	"context"
	"errors"
	"testing"
	"time"
)

const failSending = `CREATE TRIGGER no_status BEFORE UPDATE OF status ON person_messages
	BEGIN SELECT RAISE(ABORT, 'database is locked'); END`

// A status write that fails is reported, and the row is left where it was: it never reads as sent.
func TestNodeHooksReportAWriteThatFailed(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	retryDelay = time.Millisecond
	t.Cleanup(func() { retryDelay = 100 * time.Millisecond })
	queueTargeted(t, db, "ex1", "a")
	if _, err := db.Exec(failSending); err != nil {
		t.Fatal(err)
	}
	if err := NodeStarted(ctx, db, "ex1", "pub"); err == nil {
		t.Fatal("NodeStarted hid a failed write")
	}
	if err := NodeFinished(ctx, db, "ex1", "pub", nil); err == nil {
		t.Fatal("NodeFinished hid a failed write")
	}
	if s := statusOf(t, db, "a"); s != "approved" {
		t.Fatalf("a failed write moved the row to %s", s)
	}
}

// A transient error (a busy database) is retried, so the label still lands.
func TestNodeHooksRetryATransientWriteError(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	retryDelay = 40 * time.Millisecond
	t.Cleanup(func() { retryDelay = 100 * time.Millisecond })
	queueTargeted(t, db, "ex1", "a")
	if _, err := db.Exec(failSending); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(20*time.Millisecond, func() { db.Exec(`DROP TRIGGER no_status`) })
	if err := NodeStarted(ctx, db, "ex1", "pub"); err != nil {
		t.Fatalf("a transient error was not retried: %v", err)
	}
	if s := statusOf(t, db, "a"); s != "sending" {
		t.Fatalf("status = %s", s)
	}
}

// A refused move is not retried and a sent message is never sent again or turned back.
func TestNodeHooksDoNotRetryAnIllegalMove(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	queueTargeted(t, db, "ex1", "a")
	NodeStarted(ctx, db, "ex1", "pub")
	NodeFinished(ctx, db, "ex1", "pub", nil)
	if err := setStatusRetry(ctx, db, "a", Sending, Detail{}); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v", err)
	}
	if s := statusOf(t, db, "a"); s != "sent" {
		t.Fatalf("status = %s", s)
	}
}

// A query error is returned, not read as "no messages queued for this node".
func TestTargetingReturnsItsQueryError(t *testing.T) {
	db := testDB(t)
	queueTargeted(t, db, "ex1", "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := targeting(ctx, db, "ex1", "pub"); err == nil {
		t.Fatal("targeting swallowed its query error")
	}
	if err := NodeStarted(ctx, db, "ex1", "pub"); err == nil {
		t.Fatal("NodeStarted swallowed the query error")
	}
	if s := statusOf(t, db, "a"); s != "approved" {
		t.Fatalf("status = %s", s)
	}
}
