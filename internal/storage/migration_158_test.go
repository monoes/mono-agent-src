package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Migration 158 adds task_session_cap_runs.reservation_mark and backfills the cap runs that already exist with the
// newest reservation id (ids only grow, so the mark orders a run against the reservations of its session without a
// clock). These tests apply 1..157, seed rows an older binary wrote, apply 158 and let the real heartbeats judge them.

const (
	farLease = "2099-01-01T00:00:00Z"
	rowStamp = "2026-10-05T12:00:00Z"
)

type seed158 struct {
	db      *Database
	session int64
}

func open158(t *testing.T) *seed158 {
	t.Helper()
	db := open156(t)
	applyMigrationsBelow(t, db, 158)
	res, err := db.DB.Exec(`INSERT INTO task_sessions (profile_id, title, budget_usd, created_by, created_at, updated_at) VALUES ('default', 's', 10, 'you', ?, ?)`, rowStamp, rowStamp)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.DB.Exec(`UPDATE task_sessions SET state = 'running'`); err != nil {
		t.Fatal(err)
	}
	return &seed158{db: db, session: id}
}

func (s *seed158) reservation(t *testing.T, session int64, owner, state string) int64 {
	t.Helper()
	res, err := s.db.DB.Exec(`INSERT INTO task_session_reservations (profile_id, session_id, worker_ref, owner_id, amount_usd, bounded, state, lease_until, heartbeat_at, created_at)
		VALUES ('default', ?, 'w', ?, 1, 1, 'live', ?, ?, ?)`, session, owner, farLease, rowStamp, rowStamp) // a reservation starts live
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if state != "live" {
		if _, err := s.db.DB.Exec(`UPDATE task_session_reservations SET state = ? WHERE id = ?`, state, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (s *seed158) capRun(t *testing.T, session int64, owner string) int64 {
	t.Helper()
	res, err := s.db.DB.Exec(`INSERT INTO task_session_cap_runs (profile_id, session_id, owner_id, run_ref, lease_until, created_at) VALUES ('default', ?, ?, 'run', ?, ?)`,
		session, owner, farLease, rowStamp)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (s *seed158) mark(t *testing.T, capRun int64) int64 {
	t.Helper()
	var m int64
	if err := s.db.DB.QueryRow(`SELECT reservation_mark FROM task_session_cap_runs WHERE id = ?`, capRun).Scan(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A cap run that was in flight beside reservations older and newer than itself (every shape the old schema let
// through, in id order) takes the newest reservation id of the database, whatever session it is on, and keeps
// renewing; the reservations, which are not above its mark, are refused rather than put beside it.
func TestMigration158BackfillsCapRunsWithTheNewestReservationID(t *testing.T) {
	s := open158(t)
	other := func() int64 {
		res, err := s.db.DB.Exec(`INSERT INTO task_sessions (profile_id, title, budget_usd, created_by, created_at, updated_at, state) VALUES ('default', 'o', 10, 'you', ?, ?, 'running')`, rowStamp, rowStamp)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}()
	older := s.reservation(t, s.session, "o-old", "live")
	run := s.capRun(t, s.session, "o-run")
	newer := s.reservation(t, s.session, "o-new", "live")
	expired := s.reservation(t, s.session, "o-exp", "expired")
	elsewhere := s.reservation(t, other, "o-else", "settled") // newest id of all, on another session
	otherRun := s.capRun(t, other, "o-other")
	if !(older < newer && newer < expired && expired < elsewhere) {
		t.Fatalf("the seed is not in id order: %d %d %d %d", older, newer, expired, elsewhere)
	}

	if err := s.db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []int64{run, otherRun} {
		if m := s.mark(t, c); m != elsewhere {
			t.Fatalf("cap run %d: mark %d, want the newest reservation id %d", c, m, elsewhere)
		}
	}

	ctx := context.Background()
	st := tasks.NewStore(s.db.DB)
	if err := st.HeartbeatCapRun(ctx, "default", run, "o-run"); err != nil {
		t.Fatalf("an in-flight cap run is refused right after the upgrade: %v", err)
	}
	if err := st.HeartbeatCapRun(ctx, "default", otherRun, "o-other"); err != nil {
		t.Fatalf("the cap run beside only an other session's reservation: %v", err)
	}
	for name, id := range map[string]int64{"older": older, "newer": newer} {
		owner := map[string]string{"older": "o-old", "newer": "o-new"}[name]
		if err := st.Heartbeat(ctx, "default", id, owner); !errors.Is(err, tasks.ErrReservationLost) {
			t.Fatalf("the %s live reservation renewed beside the cap run: %v", name, err)
		}
	}
	if m := s.mark(t, run); m != elsewhere {
		t.Fatalf("a refused or fresh renewal moved the mark: %d", m)
	}
}

// With no reservation at all the mark is 0 (COALESCE), a reservation taken after the upgrade is above it, and the
// run is then refused: the order is the ids, not the clocks.
func TestMigration158WithNoReservationsMarksZeroAndOrdersLaterOnes(t *testing.T) {
	s := open158(t)
	run := s.capRun(t, s.session, "o-run")
	if err := s.db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if m := s.mark(t, run); m != 0 {
		t.Fatalf("mark %d, want 0", m)
	}
	ctx := context.Background()
	st := tasks.NewStore(s.db.DB)
	if err := st.HeartbeatCapRun(ctx, "default", run, "o-run"); err != nil {
		t.Fatalf("renewal beside nothing: %v", err)
	}
	s.reservation(t, s.session, "o-late", "live") // taken after the run began, however the leases read
	if err := st.HeartbeatCapRun(ctx, "default", run, "o-run"); !errors.Is(err, tasks.ErrReservationLost) {
		t.Fatalf("a reservation above the mark did not end the run: %v", err)
	}
}
