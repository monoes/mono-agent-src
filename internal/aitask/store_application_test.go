package aitask

import (
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testdb"
)

func TestForApplicationPrefersTheOpenTaskThenTheLatest(t *testing.T) {
	db := testdb.Open(t)

	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s := &Store{DB: db.DB, Profile: "p1", Now: func() time.Time { clock = clock.Add(time.Minute); return clock }}
	add := func(store *Store, app, status string) Task {
		t.Helper()
		got, err := store.Insert(Task{ID: NewID(), Kind: "generic", ConversationID: "c-" + status, Status: status, ApplicationID: app})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if got, err := s.ForApplication("app-1"); err != nil || got != nil {
		t.Fatalf("no tasks: got %v, %v", got, err)
	}
	done := add(s, "app-1", StatusComplete)
	if got, _ := s.ForApplication("app-1"); got == nil || got.ID != done.ID {
		t.Fatalf("only a finished task: want it as the latest, got %v", got)
	}
	open := add(s, "app-1", StatusRunning)
	add(s, "app-1", StatusFailed) // newer, but over
	if got, _ := s.ForApplication("app-1"); got == nil || got.ID != open.ID {
		t.Fatalf("an open task wins over a newer finished one, got %v", got)
	}
	other := &Store{DB: db.DB, Profile: "p2", Now: s.Now}
	if got, _ := other.ForApplication("app-1"); got != nil {
		t.Fatalf("another profile's task leaked: %v", got)
	}
}
