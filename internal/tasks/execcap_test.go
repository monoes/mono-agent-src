package tasks

import (
	"errors"
	"math"
	"testing"
)

func TestExecCapIsTheSmallerOfTheSessionAndItsAncestors(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	k := rkid(t, s, db, root, 4, "running")
	left, bounded, err := s.ExecCap(bg, "default", k)
	if err != nil || !bounded || math.Abs(left-4) > 1e-9 {
		t.Fatalf("child $4 under $10: %v, %v, %v", left, bounded, err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET spent_usd = 1 WHERE id = ?`, k); err != nil {
		t.Fatal(err)
	}
	if left, _, _ := s.ExecCap(bg, "default", k); math.Abs(left-3) > 1e-9 {
		t.Fatalf("child with $1 spent: %v, want 3", left)
	}
	// no budget anywhere: unbounded
	free := rsession(t, s, db, 0)
	if left, bounded, err := s.ExecCap(bg, "default", free); err != nil || bounded || left != 0 {
		t.Fatalf("no budget: %v, %v, %v", left, bounded, err)
	}
}

func TestExecCapRefusesUnderAnOverspokenAncestorAndNeverExceedsAReservation(t *testing.T) {
	s, db, _ := newTestStore(t)
	root := rsession(t, s, db, 10)
	a := rkid(t, s, db, root, 10, "running")
	rkid(t, s, db, root, 10, "running")
	if _, _, err := s.ExecCap(bg, "default", a); !errors.Is(err, ErrLimit) {
		t.Fatalf("want ErrLimit, got %v", err)
	}
	if _, err := s.Reserve(bg, "default", a, "o", "w", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a reservation is refused in the same place: %v", err)
	}
}
