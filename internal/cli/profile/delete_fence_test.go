package profile

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

type fenceFix struct {
	db   *storage.Database
	path string
	id   string
	sid  int64
}

// newFenceFix is a profile "doomed" (and one "other", so it can be deleted) with one open task session.
func newFenceFix(t *testing.T) fenceFix {
	t.Helper()
	dbPath := newProfileDocsCLITestDB(t)
	id := createTestProfile(t, dbPath, "doomed")
	createTestProfile(t, dbPath, "other")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	se, err := tasks.NewStore(db.DB).CreateSession(context.Background(), id, tasks.SessionInput{Title: "s", BudgetUSD: 10}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	return fenceFix{db: db, path: dbPath, id: id, sid: se.ID}
}

func (f fenceFix) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.db.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rawRow inserts a reservation row in the given state and lease (an RFC3339 UTC stamp).
func (f fenceFix) rawRow(t *testing.T, db *sql.DB, state, lease string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO task_session_reservations (profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at)
		VALUES (?, ?, 'w-raw', 'o', 1, 1, 'live', ?, ?, ?)`, f.id, f.sid, lease, lease, lease); err != nil {
		t.Fatal(err)
	}
	if state != "live" { // a row starts live; the sweep is what expires it
		if _, err := db.Exec(`UPDATE task_session_reservations SET state = ? WHERE id = last_insert_rowid()`, state); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteIsRefusedWhileAReservationIsLive(t *testing.T) {
	f := newFenceFix(t)
	if _, err := tasks.NewStore(f.db.DB).Reserve(context.Background(), f.id, f.sid, "owner-1", "worker-7", 2); err != nil {
		t.Fatal(err)
	}
	_, err := deleteProfile(context.Background(), f.db.DB, f.id)
	if err == nil {
		t.Fatal("deleteProfile went ahead over a live reservation")
	}
	for _, want := range []string{"worker-7", "session #", "Stop", "lapse"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q lacks %q", err, want)
		}
	}
	if f.count(t, `SELECT COUNT(*) FROM profiles WHERE id = ?`, f.id) != 1 ||
		f.count(t, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ?`, f.id) != 1 ||
		f.count(t, `SELECT COUNT(*) FROM task_session_reservations WHERE profile_id = ?`, f.id) != 1 {
		t.Fatal("a refused delete removed rows")
	}
	if _, err := runProfileCmd(t, f.path, "delete", f.id, "--yes-delete-profile", "doomed"); err == nil || !strings.Contains(err.Error(), "worker-7") {
		t.Fatalf("the command must refuse too: %v", err)
	}
}

func TestDeleteGoesAheadOverALapsedOrExpiredReservationAndTakesTheRows(t *testing.T) {
	f := newFenceFix(t)
	f.rawRow(t, f.db.DB, "live", "2026-01-01T00:00:00Z")    // past its lease and the grace: presumed dead
	f.rawRow(t, f.db.DB, "expired", "2999-01-01T00:00:00Z") // expired holds money but its owner is gone
	if _, err := deleteProfile(context.Background(), f.db.DB, f.id); err != nil {
		t.Fatalf("a lapsed or expired row must not block: %v", err)
	}
	if f.count(t, `SELECT COUNT(*) FROM task_session_reservations WHERE profile_id = ?`, f.id) != 0 ||
		f.count(t, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ?`, f.id) != 0 {
		t.Fatal("the cascade left rows")
	}
}

// A Reserve that commits between the delete's check and its first write must make the delete fail, not go ahead
// over a worker that now holds money: the deferred transaction's snapshot is stale, so its first write is refused.
func TestAReserveRacingTheDeleteFailsTheDeleteClosed(t *testing.T) {
	f := newFenceFix(t)
	other, err := storage.NewDatabase(f.path) // a second connection, as another process would have
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	afterDeleteCheck = func() {
		if _, err := tasks.NewStore(other.DB).Reserve(context.Background(), f.id, f.sid, "owner-2", "racer", 2); err != nil {
			t.Errorf("the racing reserve: %v", err)
		}
	}
	defer func() { afterDeleteCheck = func() {} }()
	_, derr := deleteProfile(context.Background(), f.db.DB, f.id)
	if derr == nil {
		t.Fatal("the delete went ahead over a reservation that committed after its check")
	}
	t.Logf("the stale transaction was refused: %v", derr)
	if f.count(t, `SELECT COUNT(*) FROM profiles WHERE id = ?`, f.id) != 1 ||
		f.count(t, `SELECT COUNT(*) FROM task_session_reservations WHERE profile_id = ?`, f.id) != 1 {
		t.Fatal("the failed delete removed rows or lost the reservation")
	}
	afterDeleteCheck = func() {}
	if _, err := deleteProfile(context.Background(), f.db.DB, f.id); err == nil || !strings.Contains(err.Error(), "racer") {
		t.Fatalf("the retry must now see the live reservation: %v", err)
	}
}
