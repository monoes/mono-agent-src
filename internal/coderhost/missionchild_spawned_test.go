package coderhost

import (
	"errors"
	"testing"
	"time"
)

// SetSpawned links the conversation the row itself has pending, or one nobody holds; never another row's.
func TestSetSpawnedCannotTakeAConversationPendingOnAnotherTask(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	f.mark(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	x := spawn(t, f.st, f.lead)
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[1], x); !errors.Is(err, ErrInvalid) {
		t.Fatalf("task 1 took task 0's pending conversation: %v", err)
	}
	if c, _, _ := f.st.Child("p", f.se.ID, f.ids[1]); c.State != ChildSpawning || c.ChildConversationID != "" {
		t.Fatalf("row 1 = %+v", c)
	}
}

func TestSetSpawnedCannotSwitchAwayFromThePendingConversation(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	x, y := spawn(t, f.st, f.lead), spawn(t, f.st, f.lead)
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], y); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the row was switched from its pending conversation: %v", err)
	}
	// the normal flow: NotePending, then SetSpawned with the same conversation
	if c, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil || c.State != ChildSpawned || c.ChildConversationID != x {
		t.Fatalf("NotePending -> SetSpawned: %+v %v", c, err)
	}
}

func TestSetSpawnedRefusesAConversationAnotherTaskRunsIn(t *testing.T) {
	f := newMC(t, true)
	f.mark(t, 0)
	f.mark(t, 1)
	for i := 0; i < 2; i++ {
		if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	x := spawn(t, f.st, f.lead)
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[0], x); err != nil { // no pending noted: accepted
		t.Fatal(err)
	}
	if _, err := f.st.SetSpawned(f.scope(), "p", f.se.ID, f.ids[1], x); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second task linked a conversation that runs another: %v", err)
	}
}

// A Touch after the stale selection keeps the reservation: the expiry is a no-op.
func TestARefreshedSpawnIsNotExpiredAfterItWasSelectedAsStale(t *testing.T) {
	f, conv := orphanFix(t)
	before := f.capOf(t, conv)
	f.st.afterStaleSelect = func() {
		f.st.afterStaleSelect = nil
		if err := f.st.Touch(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	reset, ended, err := f.st.ResetStaleSpawning(5 * time.Minute)
	if err != nil || reset != 0 || len(ended) != 0 {
		t.Fatalf("reset=%d ended=%v err=%v: a refreshed spawn was expired", reset, ended, err)
	}
	c, _, _ := f.st.Child("p", f.se.ID, f.ids[0])
	if c.State != ChildSpawning || c.ReservedUSD != 4 || f.capOf(t, conv) != before {
		t.Fatalf("row = %+v, cap %v -> %v", c, before, f.capOf(t, conv))
	}
}
