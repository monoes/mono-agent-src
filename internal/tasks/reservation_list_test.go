package tasks

import (
	"errors"
	"testing"
)

func TestSessionReservationsListsNewestFirstAndFollowsReaderRules(t *testing.T) {
	s, db, _ := newTestStore(t)
	a := rsession(t, s, db, 10)
	b := rsession(t, s, db, 10)
	r1 := rreserve(t, s, a, "o1", 2)
	r2 := rreserve(t, s, a, "o2", 3)
	rreserve(t, s, b, "o3", 1)

	rows, err := s.SessionReservations(bg, "default", a, Actor{Kind: Human})
	if err != nil || len(rows) != 2 || rows[0].ID != r2.ID || rows[1].ID != r1.ID {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
	// An agent bound to another session reads nothing of it; one bound to it reads it.
	if _, err := s.SessionReservations(bg, "default", a, Actor{Kind: Agent, Name: "w", Session: b}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other session's agent: %v", err)
	}
	if rows, err := s.SessionReservations(bg, "default", a, Actor{Kind: Agent, Name: "w", Session: a}); err != nil || len(rows) != 2 {
		t.Fatalf("own session's agent: %+v, %v", rows, err)
	}
	if _, err := s.SessionReservations(bg, "default", a, Actor{Kind: Capture}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("capture: %v", err)
	}
	if _, err := s.SessionReservations(bg, "other", a, Actor{Kind: Human}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other profile: %v", err)
	}
	if _, err := s.SessionReservations(bg, "default", 9999, Actor{Kind: Human}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
}
