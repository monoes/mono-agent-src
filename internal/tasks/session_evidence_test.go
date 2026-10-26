package tasks

import (
	"errors"
	"testing"
)

const codeNotes = "Change the parser.\n\n[mission-meta] {\"files\":[\"internal/p/p.go\"],\"size\":\"s\"}"

func claimedTask(t *testing.T, s *Store, notes, kind string) (Session, Task) {
	t.Helper()
	se := mustSession(t, s, "default", "ev")
	tk, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "work", Notes: notes, Acceptance: "`go test` passes", Kind: kind}, human)
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, s, "default", se)
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, bot("w1"), 0); err != nil {
		t.Fatal(err)
	}
	return se, tk
}

func statusOf(t *testing.T, s *Store, id int64) Status {
	t.Helper()
	tk, _, err := s.Get(bg, "default", id)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Status
}

func TestCodeTaskCannotFinishWithoutEvidence(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := claimedTask(t, s, codeNotes, "task")
	_, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1"))
	if !errors.Is(err, ErrNoEvidence) || !errors.Is(err, ErrInvalid) {
		t.Fatalf("finish without evidence: %v", err)
	}
	if st := statusOf(t, s, tk.ID); st != StatusInProgress {
		t.Fatalf("a refused finish moved the task to %s", st)
	}
	// a report is the worker's own words: not evidence
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "report", Ref: "report:w1", Note: "it works"}, bot("w1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("a report is no evidence: %v", err)
	}
	// a question needs none
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "abc1234"}, bot("w1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); err != nil {
		t.Fatalf("finish with a commit: %v", err)
	}
	if st := statusOf(t, s, tk.ID); st != StatusReview {
		t.Fatalf("status %s", st)
	}
}

func TestEvidenceRuleExemptions(t *testing.T) {
	s, _, _ := newTestStore(t)
	// research: no files declared
	se, tk := claimedTask(t, s, "Read the docs.\n\n[mission-meta] {\"size\":\"s\"}", "task")
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "found"}, bot("w1")); err != nil {
		t.Fatalf("research needs no evidence: %v", err)
	}
	// a Files: block counts as a declaration
	se2, tk2 := claimedTask(t, s, "Files:\n- a.go\n", "task")
	if _, err := s.SessionFinish(bg, "default", se2.ID, tk2.ID, Outcome{Result: "x"}, bot("w1")); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("Files: block: %v", err)
	}
	// a question needs none
	if _, err := s.SessionFinish(bg, "default", se2.ID, tk2.ID, Outcome{Question: "which one?"}, bot("w1")); err != nil {
		t.Fatalf("question: %v", err)
	}
	// a check task is exempt
	se3, tk3 := claimedTask(t, s, codeNotes, "check")
	if _, err := s.SessionFinish(bg, "default", se3.ID, tk3.ID, Outcome{Result: "ok"}, bot("w1")); err != nil {
		t.Fatalf("check: %v", err)
	}
}

func TestApproveReadiesTasksInPlanOrder(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "order")
	var want []int64
	for _, title := range []string{"one", "two", "three", "four"} {
		want = append(want, mustTask(t, s, "default", se.ID, title).ID)
	}
	mustRun(t, s, "default", se)
	var got []int64
	for range want {
		tk := claimNext(t, s, se.ID, "w1")
		if tk == nil {
			t.Fatal("ran out")
		}
		got = append(got, tk.ID)
		finishTask(t, s, se.ID, tk.ID, "w1")
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("handed out %v, planned %v", got, want)
		}
	}
}

func TestSplitPartNotesAreItsOwn(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "split")
	tk, _ := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "big", Notes: "parent notes", Acceptance: "x"}, human)
	parts, err := s.SplitTask(bg, "default", se.ID, tk.ID, []SplitPart{{Title: "a", Acceptance: "x", Notes: "own notes"}, {Title: "b", Acceptance: "y"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	if parts[0].Notes != "own notes" || parts[1].Notes != "parent notes" {
		t.Fatalf("notes: %q %q", parts[0].Notes, parts[1].Notes)
	}
}

func TestAddSessionEventRules(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustSession(t, s, "default", "a"), mustSession(t, s, "default", "b")
	w := Actor{Kind: Agent, Name: "lead", Session: a.ID}
	if err := s.AddSessionEvent(bg, "default", a.ID, EventPlanRevised, "round 1", w); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountSessionEvents(bg, "default", a.ID, EventPlanRevised, w); n != 1 {
		t.Fatalf("count %d", n)
	}
	if err := s.AddSessionEvent(bg, "default", b.ID, EventPlanRevised, "x", w); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("another session: %v", err)
	}
	for _, kind := range []string{"approved", "blocked", "closed", "split", ""} {
		if err := s.AddSessionEvent(bg, "default", a.ID, kind, "forged", w); !errors.Is(err, ErrInvalid) {
			t.Fatalf("kind %q: %v", kind, err)
		}
	}
	if n, _ := s.CountSessionEvents(bg, "default", a.ID, "approved", human); n != 0 {
		t.Fatalf("a forged approved event was written")
	}
	if n, _ := s.CountSessionEvents(bg, "default", b.ID, EventPlanRevised, human); n != 0 {
		t.Fatalf("leaked into b: %d", n)
	}
	if err := s.AddSessionEvent(bg, "default", a.ID, EventPlanRevised, "x", Actor{Kind: Capture}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("capture: %v", err)
	}
}
