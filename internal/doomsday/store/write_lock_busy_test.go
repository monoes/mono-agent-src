package store

import (
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// onWrapped returns a store over the same file whose driver lets one competing writer commit right before the first
// statement it executes from now on. Where a transaction holds no write lock yet, the writer commits and the
// transaction's own write fails with SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for); where it
// holds the lock the writer is refused (#1460).
func onWrapped(t *testing.T, s *SQLStore) (*SQLStore, func() bool) {
	t.Helper()
	var path string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil || path == "" {
		t.Fatalf("database file: %q, %v", path, err)
	}
	db, trip := dbconntest.OpenPath(t, path)
	competing := dbconntest.CompetingWriter(t, path)
	done := false
	trip.BeforeExec = func(string) {
		if !done {
			done = true
			competing()
		}
	}
	return New(db, testKeys()), func() bool { return done }
}

func TestAdvanceSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	w, fired := onWrapped(t, s)
	if _, err := w.Advance(ctx, r.Profile, r.Generation, StateStopRequested, "op", "test"); err != nil {
		t.Fatalf("Advance failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

// The chain head is read before the next row is written: a writer that commits in between must not fail the append
// with SQLITE_BUSY_SNAPSHOT (#1460).
func TestAppendAuditSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	w, fired := onWrapped(t, s)
	row, err := w.AppendAudit(ctx, r.Profile, r.Generation, "note", "{}")
	if err != nil {
		t.Fatalf("AppendAudit failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
	if row.Seq < 2 {
		t.Fatalf("seq = %d, want a row after the entry's", row.Seq)
	}
}

// ConsumeCumulative starts with its conditional insert, so there is no read before the first write and no snapshot
// for a writer to invalidate: the deferred BEGIN was already safe and it is left as it was (#1460).
func TestConsumeCumulativeStaysWriteFirst(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	w, fired := onWrapped(t, s)
	if err := w.ConsumeCumulative(ctx, r.Profile, r.Generation, "sends", "a1", 1, 5); err != nil {
		t.Fatalf("ConsumeCumulative: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}
