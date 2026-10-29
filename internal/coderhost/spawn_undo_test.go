package coderhost

import (
	"errors"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
)

func undoFix(t *testing.T) (*hostFix, ChildRow) {
	t.Helper()
	f := newHostFix(t)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[i], MarkOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	old, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil {
		t.Fatal(err)
	}
	return f, old
}

func convExists(f *hostFix, id string) bool {
	store, _ := ai.NewAIStore(f.st.db)
	_, err := store.GetConversation(id, "p")
	return err == nil
}

// The spawn's own undo leaves a conversation another reservation took through the store API in its hook.
func TestSpawnUndoKeepsAConversationAnotherRowOwns(t *testing.T) {
	f, old := undoFix(t)
	var conv string
	boom := errors.New("hook failed")
	_, err := f.st.spawnWith(f.lead, SpawnRequest{}, spawnHooks{
		sessionID: f.se.ID, taskID: f.ids[0], generation: old.Generation,
		onCreated: func(c string) error {
			conv = c
			if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], c); err != nil {
				t.Error(err)
			}
			return boom
		},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !convExists(f, conv) {
		t.Fatal("undo deleted a conversation another reservation owns")
	}
	if r := f.row(t, 1); r.PendingConversationID != conv || r.State != ChildSpawning {
		t.Fatalf("the other row = %+v, want it still holding the conversation", r)
	}
}

// A failed spawn nobody else owns still removes its conversation.
func TestSpawnUndoDeletesAnUnownedConversation(t *testing.T) {
	f, old := undoFix(t)
	for _, hk := range []spawnHooks{{}, {sessionID: f.se.ID, taskID: f.ids[0], generation: old.Generation}} {
		var conv string
		hk.onCreated = func(c string) error { conv = c; return errors.New("hook failed") }
		if _, err := f.st.spawnWith(f.lead, SpawnRequest{}, hk); err == nil {
			t.Fatal("want the hook's error")
		}
		if conv == "" || convExists(f, conv) {
			t.Fatalf("conversation %q should have been deleted", conv)
		}
	}
}

// The real handoff: a spawn that fails right after creating its conversation races a reservation that takes it.
// Whoever wins, an owned conversation is never deleted and an unowned one never stays behind.
func TestSpawnUndoRacesAReservationTakingTheConversation(t *testing.T) {
	for i := 0; i < 20; i++ {
		f, old := undoFix(t) // a fresh store each round: a store allows only a few spawns per window
		convc := make(chan string, 1)
		var wg sync.WaitGroup
		var got string
		var noteErr, spawnErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, spawnErr = f.st.spawnWith(f.lead, SpawnRequest{}, spawnHooks{
				sessionID: f.se.ID, taskID: f.ids[0], generation: old.Generation,
				onCreated:     func(c string) error { convc <- c; return nil },
				beforeMessage: func(string) error { return errors.New("late failure") }, // after the carve and the settings
			})
			if spawnErr == nil || spawnErr.Error() != "late failure" {
				convc <- "" // refused before a conversation existed
			}
		}()
		go func() {
			defer wg.Done()
			got = <-convc
			if got == "" {
				return
			}
			noteErr = f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[1], got)
		}()
		wg.Wait()
		if got == "" {
			t.Fatalf("round %d: the spawn was refused before the hook: %v", i, spawnErr)
		}
		exists := convExists(f, got)
		if noteErr == nil && !exists {
			t.Fatalf("round %d: an owned conversation was deleted", i)
		}
		if noteErr != nil && exists {
			t.Fatalf("round %d: an unowned half-created child stayed behind (%v)", i, noteErr)
		}
	}
}
