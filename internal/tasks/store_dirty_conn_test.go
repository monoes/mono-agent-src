package tasks

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
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
