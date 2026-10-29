package aitask

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func closedStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{DB: db.DB, Profile: "p1"}
}

// A cancelled task takes no new token and no new holder: a write that arrives after the
// task was closed (a start racing a deletion) must not bring it back, and the token it had
// stops working when it is cancelled.
func TestACancelledTaskTakesNoTokenAndHolder(t *testing.T) {
	s := closedStore(t)
	task, err := s.Insert(Task{ID: NewID(), Kind: "generic", ConversationID: "c1", Status: StatusRunning, Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	old := NewToken()
	if err := s.SetToken(task.ID, old); err != nil {
		t.Fatal(err)
	}
	if !s.CheckToken(task.ID, old) {
		t.Fatal("a live task's token does not work")
	}
	if moved, err := s.move(task.ID, []string{StatusRunning}, StatusCancelled, nil); err != nil || !moved {
		t.Fatalf("cancel: %v %v", moved, err)
	}
	if s.CheckToken(task.ID, old) || s.CheckTokenRemote(task.ID, old) {
		t.Error("a cancelled task's token still works")
	}

	late := NewToken()
	if err := s.SetToken(task.ID, late); err == nil {
		t.Error("SetToken on a cancelled task did not refuse")
	}
	if s.CheckToken(task.ID, late) || s.CheckTokenRemote(task.ID, late) {
		t.Error("a late token write re-enabled a cancelled task")
	}
	if err := s.SetHolder(task.ID, 4242); err == nil {
		t.Error("SetHolder on a cancelled task did not refuse")
	}
	if got, _ := s.Get(task.ID); got.Status != StatusCancelled {
		t.Errorf("status = %q", got.Status)
	}
}
