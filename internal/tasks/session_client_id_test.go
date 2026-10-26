package tasks

import (
	"errors"
	"strings"
	"testing"
)

func sessionRows(t *testing.T, s *Store, profile string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE profile_id = ?`, profile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A repeated client id in a profile returns the session it made, creates no row, and
// ignores the other fields of the repeat; the same key in another profile is its own session.
func TestCreateSessionClientIDIsIdempotentPerProfile(t *testing.T) {
	s, db, _ := newTestStore(t)
	addProfile(t, db, "other")
	first, err := s.CreateSession(bg, "default", SessionInput{Title: "One", ClientID: "k-1"}, human)
	if err != nil || first.Deduped || first.ClientID != "k-1" {
		t.Fatalf("first: %+v %v", first, err)
	}
	again, err := s.CreateSession(bg, "default", SessionInput{Title: "Changed", ClientID: "k-1"}, human)
	if err != nil || !again.Deduped || again.ID != first.ID || again.Title != "One" {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if n := sessionRows(t, s, "default"); n != 1 {
		t.Fatalf("%d rows after a repeat", n)
	}
	o, err := s.CreateSession(bg, "other", SessionInput{Title: "One", ClientID: "k-1"}, human)
	if err != nil || o.Deduped || o.ID == first.ID || sessionRows(t, s, "other") != 1 {
		t.Fatalf("other profile: %+v %v", o, err)
	}
	// without a key nothing is deduplicated, and a keyed session does not block it
	a, _ := s.CreateSession(bg, "default", SessionInput{Title: "Plain"}, human)
	b, err := s.CreateSession(bg, "default", SessionInput{Title: "Plain"}, human)
	if err != nil || a.ID == b.ID || a.Deduped || b.Deduped || a.ClientID != "" {
		t.Fatalf("unkeyed: %+v %+v %v", a, b, err)
	}
}

// The repeat is answered before the open-session limit, so a retry at the limit still works.
func TestCreateSessionClientIDRepeatAtTheLimit(t *testing.T) {
	s, _, _ := newTestStore(t)
	first, err := s.CreateSession(bg, "default", SessionInput{Title: "k", ClientID: "k"}, human)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxOpenSessions; i++ {
		mustSession(t, s, "default", "filler")
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "new"}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("at the limit: %v", err)
	}
	again, err := s.CreateSession(bg, "default", SessionInput{Title: "k", ClientID: "k"}, human)
	if err != nil || again.ID != first.ID {
		t.Fatalf("repeat at the limit: %+v %v", again, err)
	}
}

func TestCreateSessionClientIDIsValidated(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, bad := range []string{strings.Repeat("a", MaxClientIDLen+1), "has space", "a/b", "é"} {
		if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ClientID: bad}, human); !errors.Is(err, ErrInvalid) {
			t.Errorf("client id %q: %v", bad, err)
		}
	}
	if n := sessionRows(t, s, "default"); n != 0 {
		t.Fatalf("a refused key left %d sessions", n)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ClientID: strings.Repeat("a", MaxClientIDLen)}, human); err != nil {
		t.Fatalf("64 characters: %v", err)
	}
}
