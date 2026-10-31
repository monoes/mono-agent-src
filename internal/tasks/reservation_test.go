package tasks

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"
)

// rsession makes a session of the default profile with the given budget (set straight in the table, as the
// operator's set-budget is not what these tests are about).
func rsession(t *testing.T, s *Store, db *sql.DB, budget float64) int64 {
	t.Helper()
	se := mustSession(t, s, "default", "r")
	if _, err := db.Exec(`UPDATE task_sessions SET budget_usd = ? WHERE id = ?`, budget, se.ID); err != nil {
		t.Fatal(err)
	}
	return se.ID
}

// rkid makes a child session of parent with budget, in state (draft when empty).
func rkid(t *testing.T, s *Store, db *sql.DB, parent int64, budget float64, state string) int64 {
	t.Helper()
	id := rsession(t, s, db, budget)
	if state == "" {
		state = "draft"
	}
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ?, state = ? WHERE id = ?`, parent, state, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func rreserve(t *testing.T, s *Store, session int64, owner string, want float64) Reservation {
	t.Helper()
	r, err := s.Reserve(bg, "default", session, owner, "worker-"+owner, want)
	if err != nil {
		t.Fatalf("reserve %v for %s: %v", want, owner, err)
	}
	return r
}

func rrow(t *testing.T, s *Store, id int64) Reservation {
	t.Helper()
	r, err := s.reservationTx(bg, s.db, "default", id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rspent(t *testing.T, db *sql.DB, session int64) float64 {
	t.Helper()
	var v float64
	if err := db.QueryRow(`SELECT spent_usd FROM task_sessions WHERE id = ?`, session).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func rcommit(t *testing.T, s *Store, session int64) Commitment {
	t.Helper()
	c, err := s.Commitment(bg, "default", session)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestReserveGrantsTheSmallerOfWantAndLeft(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if r := rreserve(t, s, id, "a", 4); !near(r.AmountUSD, 4) || !r.Bounded || r.State != ReservationLive {
		t.Fatalf("first = %+v", r)
	}
	if r := rreserve(t, s, id, "b", 100); !near(r.AmountUSD, 6) {
		t.Fatalf("a want above what is left is cut to it: %+v", r)
	}
	if _, err := s.Reserve(bg, "default", id, "c", "w", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("nothing left: %v", err)
	}
	if left, _ := rcommit(t, s, id).Left(); left != 0 {
		t.Fatalf("left = %v", left)
	}
}

func TestReserveWantZeroTakesAllThatIsLeftAndRefusesBelowACent(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 3 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if r := rreserve(t, s, id, "a", 0); !near(r.AmountUSD, 7) || !r.Bounded {
		t.Fatalf("want 0 = %+v", r)
	}
	other := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 9.995 WHERE id = ?`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(bg, "default", other, "a", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("half a cent left: %v", err)
	}
	if n := countWhere(t, db, "task_session_reservations", "session_id = ?", other); n != 0 {
		t.Fatalf("a refused reservation left %d row(s)", n)
	}
}

// With no budget the row is still recorded, so a budget set later sees it (gap G2).
func TestReserveWithNoBudgetStillRecordsTheRow(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 0)
	capped := rreserve(t, s, id, "a", 2.5)
	free := rreserve(t, s, id, "b", 0)
	if !capped.Bounded || !near(capped.AmountUSD, 2.5) || free.Bounded || free.AmountUSD != 0 {
		t.Fatalf("capped %+v free %+v", capped, free)
	}
	if left, bounded := rcommit(t, s, id).Left(); bounded || left != 0 {
		t.Fatalf("no budget is not bounded: %v %v", left, bounded)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET budget_usd = 5 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	c := rcommit(t, s, id)
	if !near(c.Held, 2.5) {
		t.Fatalf("a budget set later sees the reservation: %+v", c)
	}
	if left, _ := c.Left(); !near(left, 2.5) {
		t.Fatalf("left = %v", left)
	}
}

func TestReserveRefusesBadInput(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	for name, c := range map[string]struct {
		owner, ref string
		want       float64
	}{
		"NaN":          {"o", "w", math.NaN()},
		"+Inf":         {"o", "w", math.Inf(1)},
		"-Inf":         {"o", "w", math.Inf(-1)},
		"negative":     {"o", "w", -1},
		"huge":         {"o", "w", 1e16},
		"empty owner":  {"", "w", 1},
		"NUL owner":    {"a\x00b", "w", 1},
		"long owner":   {strings.Repeat("o", 129), "w", 1},
		"empty ref":    {"o", "", 1},
		"NUL ref":      {"o", "w\x00", 1},
		"too long ref": {"o", strings.Repeat("w", 257), 1},
	} {
		if _, err := s.Reserve(bg, "default", id, c.owner, c.ref, c.want); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := countWhere(t, db, "task_session_reservations", "1 = 1"); n != 0 {
		t.Fatalf("%d row(s) from refused input", n)
	}
}

func TestReserveOnAClosedUnknownOrForeignSession(t *testing.T) {
	s, db, _ := newTestStore(t)
	id := rsession(t, s, db, 10)
	if _, err := db.Exec(`UPDATE task_sessions SET state = 'done' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(bg, "default", id, "o", "w", 1); !errors.Is(err, ErrNotReady) {
		t.Fatalf("closed: %v", err)
	}
	if _, err := s.Reserve(bg, "default", 9999, "o", "w", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	addProfile(t, db, "other")
	live := rsession(t, s, db, 10)
	if _, err := s.Reserve(bg, "other", live, "o", "w", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another profile's session: %v", err)
	}
}

func TestCommitmentCountsSpentHeldAndChildren(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 100)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 5 WHERE id = ?`, root); err != nil {
		t.Fatal(err)
	}
	rreserve(t, s, root, "a", 10)
	open := rkid(t, s, db, root, 20, "running") // holds its whole budget while open
	rreserve(t, s, open, "b", 3)
	closed := rkid(t, s, db, root, 30, "done") // holds only what it committed
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 2 WHERE id = ?`, closed); err != nil {
		t.Fatal(err)
	}
	over := rkid(t, s, db, root, 1, "paused") // its commitment (4) is above its budget (1)
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 4 WHERE id = ?`, over); err != nil {
		t.Fatal(err)
	}
	grand := rkid(t, s, db, open, 0, "draft") // unbudgeted: holds its commitment, which the open child carries up
	rreserve(t, s, grand, "c", 0)             // unbounded, holds nothing known
	c := rcommit(t, s, root)
	if !near(c.Spent, 5) || !near(c.Held, 10) || !near(c.Children, 20+2+4) {
		t.Fatalf("commitment = %+v", c)
	}
	if !near(c.Total(), 41) {
		t.Fatalf("total = %v", c.Total())
	}
	if left, _ := c.Left(); !near(left, 59) {
		t.Fatalf("left = %v", left)
	}
}

// A child's reservations count under its parent even when the child has no budget (it carries its commitment up).
func TestCommitmentOfAnUnbudgetedChildIsItsOwnCommitment(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 100)
	kid := rkid(t, s, db, root, 0, "running")
	rreserve(t, s, kid, "a", 7)
	if c := rcommit(t, s, root); !near(c.Children, 7) {
		t.Fatalf("%+v", c)
	}
}

func TestCommitmentWalkIsProfileScopedDepthCappedAndCycleSafe(t *testing.T) {
	s, db, _ := newTestStore(t)
	addProfile(t, db, "other")
	root := rsession(t, s, db, 100)
	// a child row that names another profile is not walked
	stray := rsession(t, s, db, 50)
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ?, profile_id = 'other' WHERE id = ?`, root, stray); err != nil {
		t.Fatal(err)
	}
	if c := rcommit(t, s, root); c.Children != 0 {
		t.Fatalf("another profile's session counted: %+v", c)
	}
	// depth: levels 1..HardMaxSessionDepth are fine, one more fails closed
	cur := root
	for i := 1; i < HardMaxSessionDepth; i++ {
		cur = rkid(t, s, db, cur, 0, "draft")
	}
	if _, err := s.Commitment(bg, "default", root); err != nil {
		t.Fatalf("a tree of %d levels: %v", HardMaxSessionDepth, err)
	}
	rkid(t, s, db, cur, 0, "draft")
	if _, err := s.Commitment(bg, "default", root); !errors.Is(err, ErrLimit) {
		t.Fatalf("a tree one level too deep: %v", err)
	}
	// a cycle fails closed instead of looping
	a := rsession(t, s, db, 0)
	b := rkid(t, s, db, a, 0, "draft")
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b, a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commitment(bg, "default", a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a cycle: %v", err)
	}
}
