package tasks

import (
	"errors"
	"testing"
	"time"
)

// A mission worker's token names its session and its task: it works that task and nothing
// else, even inside its own session.
func TestActorBoundToATaskTouchesOnlyThatTask(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, ts := runningWith(t, s, "mine", "theirs")
	mine, theirs := ts[0], ts[1]
	worker := Actor{Kind: Agent, Name: "w1", Session: se.ID, Task: mine.ID}

	if _, err := s.SessionClaim(bg, "default", se.ID, theirs.ID, worker, 0); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("claim of another task of its own session: %v", err)
	}
	if tk, _, _ := s.Get(bg, "default", theirs.ID); tk.Status != StatusReady || tk.Claim != nil {
		t.Fatalf("the refused claim changed the other task: %+v", tk)
	}
	if got, err := s.SessionNext(bg, "default", se.ID, worker, true, 0); !errors.Is(err, ErrOperatorOnly) || got != nil {
		t.Fatalf("a bound worker takes what it is given, not the next: %v %v", got, err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "extra", Acceptance: "x"}, worker); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a bound worker adds no task: %v", err)
	}
	if n := countWhere(t, db, "tasks", "session_id = ?", se.ID); n != 2 {
		t.Fatalf("tasks in the session = %d, want 2", n)
	}

	// someone else holds `theirs`: the worker cannot finish it, nor comment on it
	if _, err := s.SessionClaim(bg, "default", se.ID, theirs.ID, bot("other"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, theirs.ID, Outcome{Result: "stolen"}, worker); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("finish of another task: %v", err)
	}
	if _, err := s.SessionComment(bg, "default", se.ID, theirs.ID, "hi", worker); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("comment on another task: %v", err)
	}
	if tk, _, _ := s.Get(bg, "default", theirs.ID); tk.Status != StatusInProgress {
		t.Fatalf("the other task moved: %+v", tk)
	}

	// its own task it can claim, comment on, attach to and finish
	if _, err := s.SessionClaim(bg, "default", se.ID, mine.ID, worker, 0); err != nil {
		t.Fatalf("claim own: %v", err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, mine.ID, ArtifactInput{Kind: "report", Ref: "r"}, worker); err != nil {
		t.Fatalf("artifact own: %v", err)
	}
	if tk, err := s.SessionFinish(bg, "default", se.ID, mine.ID, Outcome{Result: "done"}, worker); err != nil || tk.Status != StatusReview {
		t.Fatalf("finish own: %+v %v", tk, err)
	}
}

func TestSessionReadyListsWhatNextWouldHandOut(t *testing.T) {
	s, _, clk := newTestStore(t)
	se := mustSession(t, s, "default", "ready")
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	c := mustTask(t, s, "default", se.ID, "c")
	if got, _ := s.SessionReady(bg, "default", se.ID, bot("x")); len(got) != 0 {
		t.Fatalf("a draft has nothing ready: %+v", got)
	}
	mustRun(t, s, "default", se)
	ids := func() []int64 {
		got, err := s.SessionReady(bg, "default", se.ID, bot("x"))
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, tk := range got {
			out = append(out, tk.ID)
		}
		return out
	}
	got := ids()
	if len(got) != 2 || !(got[0] == a.ID && got[1] == c.ID || got[0] == c.ID && got[1] == a.ID) {
		t.Fatalf("a and c are ready, b waits for a: %v (b=%d)", got, b.ID)
	}
	if next, _ := s.SessionNext(bg, "default", se.ID, bot("x"), false, 0); next == nil || next.ID != got[0] {
		t.Fatalf("SessionNext offers the head of the list: %+v vs %v", next, got)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, a.ID, bot("w1"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := ids(); len(got) != 1 || got[0] != c.ID {
		t.Fatalf("a is held: %v", got)
	}
	clk.advance(2 * time.Minute)
	if got := ids(); len(got) != 2 || got[0] != c.ID || got[1] != a.ID {
		t.Fatalf("a's lease ended: it is offered again, after the Ready ones: %v", got)
	}
	if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if got := ids(); len(got) != 0 {
		t.Fatalf("a paused session hands out nothing: %v", got)
	}
}

func TestSetAssigneeNeedsTheHolder(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "t")
	if _, err := s.SetAssignee(bg, "default", se.ID, ts[0].ID, "worker:w1", bot("w1")); !errors.Is(err, ErrNotClaimant) {
		t.Fatalf("an agent that does not hold it: %v", err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, ts[0].ID, bot("w1"), 0); err != nil {
		t.Fatal(err)
	}
	tk, err := s.SetAssignee(bg, "default", se.ID, ts[0].ID, "worker:w1", bot("w1"))
	if err != nil || tk.Assignee != "worker:w1" {
		t.Fatalf("%+v %v", tk, err)
	}
}

func TestBoundWorkerAndVerifierCannotChangeDependencies(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "mine", "another")
	for _, verifier := range []bool{false, true} {
		actor := Actor{Kind: Agent, Name: "bound", Session: se.ID, Task: ts[0].ID, Verifier: verifier}
		for _, id := range []int64{ts[0].ID, ts[1].ID} {
			on := ts[0].ID
			if id == on {
				on = ts[1].ID
			}
			if _, err := s.AddDependencies(bg, "default", se.ID, id, []int64{on}, actor); !errors.Is(err, ErrOperatorOnly) {
				t.Fatalf("verifier=%t changed dependencies of %d: %v", verifier, id, err)
			}
		}
	}
	for _, tk := range ts {
		detail, err := s.SessionTask(bg, "default", se.ID, tk.ID, human)
		if err != nil || len(detail.Task.DependsOn) != 0 {
			t.Fatalf("refusal changed dependencies: %+v %v", detail, err)
		}
	}
	if _, err := s.AddDependencies(bg, "default", se.ID, ts[1].ID, []int64{ts[0].ID}, bot("lead")); err != nil {
		t.Fatalf("unbound lead cannot add dependencies: %v", err)
	}
}
