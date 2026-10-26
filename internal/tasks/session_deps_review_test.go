package tasks

import (
	"errors"
	"strings"
	"testing"
)

// Finding 6: archiving a task that open tasks wait for is refused and names them.
func TestArchiveRefusedWhileOthersDependOnIt(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "arch")
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	_, err := s.Archive(bg, "default", []int64{a.ID}, human)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "#"+itoa(b.ID)) {
		t.Fatalf("archive: %v", err)
	}
	if got, _, _ := s.Get(bg, "default", a.ID); got.Status == StatusArchived {
		t.Fatal("archived anyway")
	}
	// the dependent goes with it in the same call: fine
	if _, err := s.Archive(bg, "default", []int64{a.ID, b.ID}, human); err != nil {
		t.Fatalf("archive both: %v", err)
	}
}

// Finding 6: a dependent that is done no longer holds its dependency back from the archive.
func TestArchiveAllowedWhenDependentsAreDone(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "arch2")
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	if _, err := s.Move(bg, "default", b.ID, StatusDone, Placement{}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive(bg, "default", []int64{a.ID}, human); err != nil {
		t.Fatalf("archive: %v", err)
	}
}

// Finding 6: reopening a done task takes its verified mark: it no longer settles its dependents.
func TestReopenedTaskIsNoLongerVerified(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "reopen")
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	mustRun(t, s, "default", se)
	if _, err := s.SessionClaim(bg, "default", se.ID, a.ID, bot("w1"), 0); err != nil {
		t.Fatal(err)
	}
	finishTask(t, s, se.ID, a.ID, "w1")
	if got, err := s.VerifySessionTask(bg, "default", se.ID, a.ID, true, "", bot("c")); err != nil || !got.Verified {
		t.Fatalf("verify: %+v %v", got, err)
	}
	for _, to := range []Status{StatusDone, StatusReview} {
		if _, err := s.Move(bg, "default", a.ID, to, Placement{}, human); err != nil {
			t.Fatal(err)
		}
	}
	got, _, _ := s.Get(bg, "default", a.ID)
	if got.Verified || got.Status != StatusReview {
		t.Fatalf("reopened: %+v", got)
	}
	if nx, _ := s.SessionNext(bg, "default", se.ID, bot("w2"), false, 0); nx != nil {
		t.Fatalf("#%d (waiting for #%d) is handed out while it is open again", nx.ID, a.ID)
	}
	_ = b
}

// Finding 6: nothing is made to wait for an epic, which never settles.
func TestDependencyOnAnEpicIsRefused(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "epic")
	a, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "a", Kind: "epic", Acceptance: "x"}, human)
	if err != nil {
		t.Fatal(err)
	}
	c := mustTask(t, s, "default", se.ID, "c")
	gone := mustTask(t, s, "default", se.ID, "gone")
	if _, err := s.Archive(bg, "default", []int64{gone.ID}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDependencies(bg, "default", se.ID, c.ID, []int64{gone.ID}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("dependency on an archived task: %v", err)
	}
	if _, err := s.AddDependencies(bg, "default", se.ID, c.ID, []int64{a.ID}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("add dependency: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "d", Acceptance: "x", DependsOn: []int64{a.ID}}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("add task: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_deps WHERE depends_on_task_id = ?`, a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("dependency recorded: %d %v", n, err)
	}
}
