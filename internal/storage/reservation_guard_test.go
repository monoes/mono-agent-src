package storage

import (
	"database/sql"
	"testing"
)

func mustSession2(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO task_sessions (id, profile_id, title, created_by, created_at, updated_at) VALUES (2, 'p', 's', 'you', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
}

func resCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + resTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// endRes moves a live row to state through the same UPDATE the store makes.
func endRes(t *testing.T, db *sql.DB, id int64, state string) {
	t.Helper()
	q := `UPDATE ` + resTable + ` SET state = ?, ended_at = ? WHERE id = ?`
	if _, err := db.Exec(q, state, goodTS, id); err != nil {
		t.Fatal(err)
	}
}

// A reservation is never removed by hand while its session exists, in any state (ended rows are the audit
// trail); only the cascade of removing the session or the profile takes them.
func TestReservationCannotBeDeletedDirectlyWhileItsSessionExists(t *testing.T) {
	db := resDB(t).DB
	live := mustRes(t, db, nil)
	expired := mustRes(t, db, nil)
	endRes(t, db, expired, "expired")
	settled := mustRes(t, db, nil)
	endRes(t, db, settled, "settled")
	released := mustRes(t, db, nil)
	endRes(t, db, released, "released")
	resolved := mustRes(t, db, nil)
	endRes(t, db, resolved, "expired")
	endRes(t, db, resolved, "resolved")
	for name, id := range map[string]int64{"live": live, "expired": expired, "settled": settled, "released": released, "resolved": resolved} {
		_, err := db.Exec(`DELETE FROM `+resTable+` WHERE id = ?`, id)
		wantRefused(t, "delete "+name, err)
	}
	_, err := db.Exec(`DELETE FROM ` + resTable)
	wantRefused(t, "delete all", err)
	if n := resCount(t, db); n != 5 {
		t.Fatalf("%d rows left, want 5", n)
	}
}

func TestReservationsOfEveryStateCascadeWithTheirSession(t *testing.T) {
	db := resDB(t).DB
	mustSession2(t, db)
	for _, st := range []string{"live", "expired", "settled"} {
		id := mustRes(t, db, map[string]any{"session_id": 2})
		if st != "live" {
			endRes(t, db, id, st)
		}
	}
	mustRes(t, db, nil)
	if _, err := db.Exec(`DELETE FROM task_sessions WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if n := resCount(t, db); n != 1 {
		t.Fatalf("%d rows after removing the session, want 1", n)
	}
}

func TestReservationsOfEveryStateCascadeWithTheirProfile(t *testing.T) {
	db := resDB(t).DB
	mustSession2(t, db)
	for _, sid := range []int{1, 2} {
		mustRes(t, db, map[string]any{"session_id": sid})
		id := mustRes(t, db, map[string]any{"session_id": sid})
		endRes(t, db, id, "expired")
		id = mustRes(t, db, map[string]any{"session_id": sid})
		endRes(t, db, id, "released")
	}
	if _, err := db.Exec(`DELETE FROM profiles WHERE id = 'p'`); err != nil {
		t.Fatal(err)
	}
	if n := resCount(t, db); n != 0 {
		t.Fatalf("%d rows after removing the profile", n)
	}
}

// The profile of a reservation is the profile of its session.
func TestReservationProfileMustBeTheSessionsProfile(t *testing.T) {
	db := resDB(t).DB
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES ('p2', 'P2')`); err != nil {
		t.Fatal(err)
	}
	_, err := insertRes(db, "INSERT", map[string]any{"profile_id": "p2"})
	wantRefused(t, "profile p2 on p's session", err)
	if n := resCount(t, db); n != 0 {
		t.Fatalf("%d rows", n)
	}
	mustRes(t, db, nil)
}

// Only the table hands out ids: an explicit one (which could sit near the top and exhaust AUTOINCREMENT) is refused.
func TestReservationExplicitIdsAreRefused(t *testing.T) {
	db := resDB(t).DB
	for _, id := range []int64{9223372036854775800, 5, 1} {
		for _, verb := range []string{"INSERT", "INSERT OR REPLACE", "INSERT OR IGNORE"} {
			_, err := insertRes(db, verb, map[string]any{"id": id})
			wantRefused(t, verb+" with an explicit id", err)
		}
	}
	if n := resCount(t, db); n != 0 {
		t.Fatalf("%d rows", n)
	}
	if id := mustRes(t, db, nil); id != 1 {
		t.Fatalf("first generation %d", id)
	}
}

// A live row has not ended: ended_at cannot be set while the state is live.
func TestReservationLiveRowCannotCarryAnEndTime(t *testing.T) {
	db := resDB(t).DB
	id := mustRes(t, db, nil)
	for _, q := range []string{
		`UPDATE ` + resTable + ` SET ended_at = '` + goodTS + `' WHERE id = ?`,
		`UPDATE ` + resTable + ` SET state = 'live', ended_at = '` + goodTS + `' WHERE id = ?`,
	} {
		_, err := db.Exec(q, id)
		wantRefused(t, q, err)
	}
	endRes(t, db, id, "released")
}

// The guard is 1e12, where a cent is still representable.
func TestReservationAmountsUpToTheOverflowGuardAreAccepted(t *testing.T) {
	db := resDB(t).DB
	mustRes(t, db, map[string]any{"amount_usd": 1e12})
	_, err := insertRes(db, "INSERT", map[string]any{"amount_usd": 2e12})
	wantRefused(t, "an amount above the guard", err)
	_, err = insertRes(db, "INSERT", map[string]any{"amount_usd": 1e12 + 1})
	wantRefused(t, "an amount just above the guard", err)
}
