package tasks

import (
	"testing"
	"time"
)

// A release made on the strength of an old read of the claim must not free a claim that
// was released and taken again under the same name since (the order is set by hand).
func TestSessionReleaseIfHeldLeavesANewClaimOfTheSameName(t *testing.T) {
	s, _, clk := newTestStore(t)
	se := mustSession(t, s, "default", "rel")
	tk := mustTask(t, s, "default", se.ID, "work")
	mustRun(t, s, "default", se)
	w := Actor{Kind: Agent, Name: "m1-t1", Session: se.ID, Task: tk.ID}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, w, time.Minute); err != nil {
		t.Fatal(err)
	}
	old, _, _ := s.Get(bg, "default", tk.ID) // the stale tick's read
	oldClaim := *old.Claim

	// the claim ends, the turn gives it up and a restarted turn takes it again, same name
	clk.advance(10 * time.Minute)
	if _, err := s.SessionRelease(bg, "default", se.ID, tk.ID, "turn ended", w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, w, time.Minute); err != nil {
		t.Fatal(err)
	}

	done, err := s.SessionReleaseIfHeld(bg, "default", se.ID, tk.ID, oldClaim, "stale tick", w)
	if err != nil || done {
		t.Fatalf("stale release: done=%v err=%v", done, err)
	}
	got, evs, _ := s.Get(bg, "default", tk.ID)
	if got.Status != StatusInProgress || got.Claim == nil || got.Claim.By != "m1-t1" || !got.Claim.Until.After(oldClaim.Until) {
		t.Fatalf("the new claim was freed or changed: %+v", got)
	}
	for _, e := range evs {
		if e.Note == "stale tick" {
			t.Fatalf("an event was written for the refused release: %+v", e)
		}
	}

	// the claim as it is now is given back
	done, err = s.SessionReleaseIfHeld(bg, "default", se.ID, tk.ID, *got.Claim, "current", w)
	if err != nil || !done {
		t.Fatalf("current release: done=%v err=%v", done, err)
	}
	if got, _, _ := s.Get(bg, "default", tk.ID); got.Status != StatusReady || got.Claim != nil {
		t.Fatalf("not released: %+v", got)
	}
	// a task nobody holds is a no-op as well
	if done, err := s.SessionReleaseIfHeld(bg, "default", se.ID, tk.ID, oldClaim, "again", w); err != nil || done {
		t.Fatalf("release of a free task: done=%v err=%v", done, err)
	}
}

// The claim read earlier names its holder as well as its lease end: a claim of another
// holder, with the lease end of the one held, is not given back.
func TestSessionReleaseIfHeldComparesTheHolder(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "rel2")
	tk := mustTask(t, s, "default", se.ID, "work")
	mustRun(t, s, "default", se)
	w := Actor{Kind: Agent, Name: "m1-t1", Session: se.ID, Task: tk.ID}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, w, time.Minute); err != nil {
		t.Fatal(err)
	}
	cur, _, _ := s.Get(bg, "default", tk.ID)
	other := *cur.Claim
	other.By = "someone-else"
	if done, err := s.SessionReleaseIfHeld(bg, "default", se.ID, tk.ID, other, "x", w); err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if got, _, _ := s.Get(bg, "default", tk.ID); got.Claim == nil || got.Status != StatusInProgress {
		t.Fatalf("released: %+v", got)
	}
}
