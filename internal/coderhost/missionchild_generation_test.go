package coderhost

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// A reservation's identity is its generation, not the attempt count: a refused spawn gives its attempt back, so
// the next reservation of the task has the same attempt number but a new generation. These tests build exactly
// that: an earlier reservation that was given back, and a newer one that holds the row, and check that nothing
// the earlier spawn still does reaches the newer one.

// twoReservationsSameAttempt reserves the task, gives the reservation back as a refusal (attempts go back down)
// and reserves it again. It returns the earlier and the newer row.
func twoReservationsSameAttempt(t *testing.T) (*hostFix, ChildRow, ChildRow) {
	t.Helper()
	f := newHostFix(t)
	f.mark(t, 0)
	old, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], old.Generation, f.lead.ConversationID, false, "refused"); err != nil {
		t.Fatal(err)
	}
	newer, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if newer.Attempts != old.Attempts {
		t.Fatalf("setup: the attempt count should be the same (old %d, new %d)", old.Attempts, newer.Attempts)
	}
	if newer.Generation <= old.Generation {
		t.Fatalf("a newer reservation must have a higher generation: old %d, new %d", old.Generation, newer.Generation)
	}
	return f, old, newer
}

func TestReservationGenerationNeverRepeats(t *testing.T) {
	f := newHostFix(t)
	f.mark(t, 0)
	seen := map[int64]bool{}
	for i := 0; i < 2; i++ { // reserve, give back, reserve again
		r, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
		if err != nil {
			t.Fatal(err)
		}
		if r.Generation <= 0 || seen[r.Generation] {
			t.Fatalf("generation %d is not new", r.Generation)
		}
		seen[r.Generation] = true
		if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], r.Generation, f.lead.ConversationID, false, "refused"); err != nil {
			t.Fatal(err)
		}
	}
	// A row that is deleted and made again starts no lower than the generations already given out.
	if _, err := f.st.db.Exec(`DELETE FROM coder_mission_children WHERE profile_id = ? AND session_id = ? AND task_id = ?`, "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	f.mark(t, 0)
	r, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if seen[r.Generation] {
		t.Fatalf("a recreated row reused generation %d", r.Generation)
	}
}

// Each callback of the earlier spawn, one test per fence, leaves the newer row exactly as it was.

func TestStaleGenerationCannotTouchNewReservation(t *testing.T) {
	f, old, newer := twoReservationsSameAttempt(t)
	f.st.now = func() time.Time { return time.Now().Add(time.Minute) } // a changed clock would show in updated_at
	if err := f.st.touch(f.scope(), "p", f.se.ID, f.ids[0], old.Generation); err == nil {
		t.Fatal("the earlier reservation touched the newer one")
	}
	if after := f.row(t, 0); !reflect.DeepEqual(newer, after) {
		t.Fatalf("newer row changed: before=%+v after=%+v", newer, after)
	}
}

func TestStaleGenerationCannotNotePendingOnNewReservation(t *testing.T) {
	f, old, newer := twoReservationsSameAttempt(t)
	conv := spawn(t, f.st, f.lead)
	if err := f.st.notePending(f.scope(), "p", f.se.ID, f.ids[0], conv, old.Generation); err == nil {
		t.Fatal("the earlier reservation set a pending conversation on the newer one")
	}
	if after := f.row(t, 0); !reflect.DeepEqual(newer, after) {
		t.Fatalf("newer row changed: before=%+v after=%+v", newer, after)
	}
}

func TestStaleGenerationCannotLinkNewReservation(t *testing.T) {
	f, old, newer := twoReservationsSameAttempt(t)
	conv := spawn(t, f.st, f.lead)
	if _, err := f.st.setSpawned(f.scope(), "p", f.se.ID, f.ids[0], conv, old.Generation); err == nil {
		t.Fatal("the earlier reservation linked its conversation to the newer one")
	}
	if after := f.row(t, 0); !reflect.DeepEqual(newer, after) {
		t.Fatalf("newer row changed: before=%+v after=%+v", newer, after)
	}
}

func TestStaleGenerationCannotReleaseNewReservation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "failed"}[failed], func(t *testing.T) {
			f, old, newer := twoReservationsSameAttempt(t)
			if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], old.Generation, f.lead.ConversationID, failed, "old cleanup"); err != nil {
				t.Fatal(err)
			}
			if after := f.row(t, 0); !reflect.DeepEqual(newer, after) {
				t.Fatalf("the earlier cleanup changed the newer reservation: before=%+v after=%+v", newer, after)
			}
		})
	}
}

// The same through the unwinding of a real spawn (abandonSpawn with the conversation it made).
func TestStaleAbandonSpawnCannotFailNewReservation(t *testing.T) {
	f := newHostFix(t)
	f.mark(t, 0)
	old, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	oldConv := spawn(t, f.st, f.lead)
	if err := f.st.notePending(f.scope(), "p", f.se.ID, f.ids[0], oldConv, old.Generation); err != nil {
		t.Fatal(err)
	}
	// spawnWith's carveRefused callback releases before the spawn's own undo runs.
	if err := f.st.releaseReservation("p", f.se.ID, f.ids[0], old.Generation, f.lead.ConversationID, false, "carve refused"); err != nil {
		t.Fatal(err)
	}
	newer, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	_ = f.st.abandonSpawn(f.lead, "p", f.se.ID, f.ids[0], old.Generation, oldConv, fmt.Errorf("%w: carve refused", ErrLimit))
	if after := f.row(t, 0); !reflect.DeepEqual(newer, after) {
		t.Fatalf("the earlier spawn's cleanup changed the newer reservation: before=%+v after=%+v", newer, after)
	}
}

// End to end: a real SpawnForTask whose carve is refused (the lead's budget drops after the child exists) while
// a newer Mark+Reserve of the same task lands in the middle of its unwinding.
func TestSpawnForTaskCarveRefusedDoesNotFailInterleavedReservation(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 3}); err != nil {
		t.Fatal(err)
	}
	newerReserved := false
	f.st.afterChildCreated = func() {
		if err := f.st.SetBudget("p", f.lead.ConversationID, 0.5); err != nil {
			t.Error(err)
			return
		}
		original := f.st.now
		f.st.now = func() time.Time {
			if row, _, err := f.st.Child("p", f.se.ID, f.ids[0]); err == nil && row.State == ChildGone && row.Attempts == 1 { // the delete ended the row; the release has not run yet
				f.st.now = original
				if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 0.1}); err != nil {
					t.Error(err)
				}
				if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
					t.Error(err)
				}
				newerReserved = true
			}
			return original()
		}
	}
	if _, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "old spawn"); err == nil {
		t.Fatal("the carve should have been refused")
	}
	if !newerReserved {
		t.Fatal("the interleaved reservation did not run")
	}
	if row := f.row(t, 0); row.State != ChildSpawning || row.ReservedUSD == 0 {
		t.Fatalf("the earlier spawn's cleanup destroyed the newer reservation: %+v", row)
	}
}
