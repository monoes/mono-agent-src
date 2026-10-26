package tasks

import (
	"errors"
	"strings"
	"testing"
)

// An epic only groups its children and never reaches Done: it is not progress, so a finished
// session reads 2/2, not 2/3, while max_tasks still counts it.
func TestSessionCountsLeaveOutEpics(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "epics")
	epic, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "epic", Acceptance: "x", Kind: KindEpic}, human)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "a", Acceptance: "x", ParentID: epic.ID}, human)
	if err != nil {
		t.Fatal(err)
	}
	b := mustTask(t, s, "default", se.ID, "b")
	mustRun(t, s, "default", se)
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := s.Move(bg, "default", id, StatusDone, Placement{}, human); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetSession(bg, "default", se.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	c := got.Counts
	if c.Done != 2 || c.Total != 2 || c.Ready != 0 || c.Epics != 1 || c.Stored() != 3 {
		t.Fatalf("counts %+v", c)
	}
	// the plan view still shows the epic
	plan, err := s.PlanOfSession(bg, "default", se.ID, human)
	if err != nil || len(plan.Tasks) != 3 {
		t.Fatalf("plan %d tasks, %v", len(plan.Tasks), err)
	}
}

func TestSessionMaxTasksStillCountsEpics(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "cap", Goal: "g", MaxTasks: 2}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "epic", Acceptance: "x", Kind: KindEpic}, human); err != nil {
		t.Fatal(err)
	}
	mustTask(t, s, "default", se.ID, "a")
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "b", Acceptance: "x"}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("the third stored task: %v", err)
	}
}

// After a failed verification the old commit is the work that failed: a retry needs evidence
// attached after the failure.
func TestRetryNeedsEvidenceNewerThanTheFailedVerification(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := claimedTask(t, s, codeNotes, "task")
	w := bot("w1")
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "old1234"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, false, "the test fails", bot("checker")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, w, 0); err != nil {
		t.Fatal(err)
	}
	_, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "fixed"}, w)
	if !errors.Is(err, ErrNoEvidence) || !strings.Contains(err.Error(), "failed verification") {
		t.Fatalf("a retry on the old commit: %v", err)
	}
	if st := statusOf(t, s, tk.ID); st != StatusInProgress {
		t.Fatalf("status %s", st)
	}
	// a report is still no evidence; a new commit is
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "report", Ref: "r"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "fixed"}, w); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("a report after the failure: %v", err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "new5678"}, w); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "fixed"}, w); err != nil || got.Status != StatusReview {
		t.Fatalf("with new evidence: %+v %v", got, err)
	}
}

// A worker bound to a task, and a verifier, cannot pause or stop the whole session; the lead
// (bound to the session only) and the operator can.
func TestBoundWorkersAndVerifiersCannotPauseOrStopTheSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "a")
	worker := Actor{Kind: Agent, Name: "w1", Session: se.ID, Task: ts[0].ID}
	verifier := Actor{Kind: Agent, Name: "v1", Session: se.ID, Task: ts[0].ID, Verifier: true}
	for _, a := range []Actor{worker, verifier} {
		if _, err := s.PauseSession(bg, "default", se.ID, a); !errors.Is(err, ErrOperatorOnly) || !strings.Contains(err.Error(), "bound to task #") {
			t.Fatalf("%s pauses: %v", a.Name, err)
		}
		if _, err := s.StopSession(bg, "default", se.ID, a); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%s stops: %v", a.Name, err)
		}
	}
	if got := mustSessionState(t, s, se.ID); got != SessionRunning {
		t.Fatalf("the session is %s", got)
	}
	lead := Actor{Kind: Agent, Name: "lead", Session: se.ID}
	if got, err := s.PauseSession(bg, "default", se.ID, lead); err != nil || got.State != SessionPaused {
		t.Fatalf("the lead pauses: %+v %v", got, err)
	}
	if got, err := s.StopSession(bg, "default", se.ID, human); err != nil || got.State != SessionStopped {
		t.Fatalf("the operator stops: %+v %v", got, err)
	}
}

func mustSessionState(t *testing.T, s *Store, id int64) SessionState {
	t.Helper()
	se, err := s.GetSession(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	return se.State
}

// Finish and artifact are refused while the session is paused or stopped; a comment is not.
func TestFinishAndArtifactRefusedWhileSessionIsNotRunning(t *testing.T) {
	for _, stop := range []bool{false, true} {
		s, _, _ := newTestStore(t)
		se, ts := runningWith(t, s, "a")
		claimNext(t, s, se.ID, "w")
		var err error
		word := "paused"
		if stop {
			word = "stopped"
			_, err = s.StopSession(bg, "default", se.ID, human)
		} else {
			_, err = s.PauseSession(bg, "default", se.ID, human)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddArtifact(bg, "default", se.ID, ts[0].ID, ArtifactInput{Kind: "report", Ref: "r"}, bot("w")); !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), word) {
			t.Fatalf("%s: artifact: %v", word, err)
		}
		if n := len(mustDetail(t, s, se.ID, ts[0].ID).Artifacts); n != 0 {
			t.Fatalf("%s: a refused artifact was recorded", word)
		}
		if _, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "x"}, bot("w")); !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), word) {
			t.Fatalf("%s: finish: %v", word, err)
		}
		if !stop {
			if _, err := s.SessionComment(bg, "default", se.ID, ts[0].ID, "note", bot("w")); err != nil {
				t.Fatalf("a comment while paused: %v", err)
			}
		}
	}
}

func mustDetail(t *testing.T, s *Store, sid, id int64) SessionTaskDetail {
	t.Helper()
	d, err := s.SessionTask(bg, "default", sid, id, human)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A refusal in a session says what is wrong in the session's words, and keeps its sentinel.
func TestSessionRefusalsAreWordedForSessions(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "a", "b")
	if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	_, err := s.PauseSession(bg, "default", se.ID, human)
	if !errors.Is(err, ErrNotReady) || strings.Contains(err.Error(), "cannot be claimed") || !strings.Contains(err.Error(), "session #") {
		t.Fatalf("double pause: %v", err)
	}
	if _, err := s.StopSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopSession(bg, "default", se.ID, human); !errors.Is(err, ErrNotReady) || strings.Contains(err.Error(), "cannot be claimed") {
		t.Fatalf("double stop: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "c", Acceptance: "x"}, human); !errors.Is(err, ErrNotReady) || strings.Contains(err.Error(), "cannot be claimed") {
		t.Fatalf("add to a stopped session: %v", err)
	}
	// a bound violation
	worker := Actor{Kind: Agent, Name: "w1", Session: se.ID, Task: ts[0].ID}
	_, err = s.SessionClaim(bg, "default", se.ID, ts[1].ID, worker, 0)
	if !errors.Is(err, ErrOperatorOnly) || strings.Contains(err.Error(), "only the operator can do that") ||
		!strings.Contains(err.Error(), "bound to task #"+itoa(ts[0].ID)) {
		t.Fatalf("bound violation: %v", err)
	}
	// the personal board keeps its words
	mine := mustAdd(t, s, "default", "mine", true)
	if _, err := s.Claim(bg, "default", mine.ID, bot("w"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(bg, "default", mine.ID, bot("w2"), 0); !errors.Is(err, ErrClaimed) {
		t.Fatalf("personal board: %v", err)
	}
}

func TestSessionTitleOverTheLimitIsRefusedNotCut(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "titles")
	long := strings.Repeat("x", MaxTitleRunes+1)
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: long, Acceptance: "x"}, human); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "201") {
		t.Fatalf("a title of 201 characters: %v", err)
	}
	if got := mustDetail2(t, s, se.ID); got != 0 {
		t.Fatalf("a refused task was written: %d", got)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: strings.Repeat("y", MaxTitleRunes), Acceptance: "x"}, human); err != nil {
		t.Fatalf("a title of exactly the limit: %v", err)
	}
}

func mustDetail2(t *testing.T, s *Store, sid int64) int {
	t.Helper()
	ts, err := s.SessionTasks(bg, "default", sid, Filter{}, human)
	if err != nil {
		t.Fatal(err)
	}
	return len(ts)
}

// A claim of a task that is not ready, in a session, is refused in the session's words
// (still ErrNotReady); on the personal board the sentinel's text stays.
func TestSessionClaimRefusalDropsThePersonalBoardWording(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "a", "b")
	w := Actor{Kind: Agent, Name: "w1"}
	if _, err := s.SessionClaim(bg, "default", se.ID, ts[0].ID, w, 0); err != nil {
		t.Fatal(err)
	}
	finishTask(t, s, se.ID, ts[0].ID, "w1") // now in review
	_, err := s.SessionClaim(bg, "default", se.ID, ts[0].ID, Actor{Kind: Agent, Name: "w2"}, 0)
	if !errors.Is(err, ErrNotReady) || strings.Contains(err.Error(), "task cannot be claimed") || !strings.Contains(err.Error(), "#"+itoa(ts[0].ID)) {
		t.Fatalf("claim of a task in review: %v", err)
	}
	mine := mustAdd(t, s, "default", "mine", false) // inbox
	_, err = s.Claim(bg, "default", mine.ID, bot("w"), 0)
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "task cannot be claimed") {
		t.Fatalf("personal board: %v", err)
	}
}

// CLI and MCP route task_finish through FinishAny. Its transaction must enforce
// the same pause as SessionFinish, for both results and questions.
func TestFinishAnyCannotFinishPausedSession(t *testing.T) {
	for _, outcome := range []Outcome{{Result: "done"}, {Question: "what next?"}} {
		t.Run(outcome.Result+outcome.Question, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			se, ts := runningWith(t, s, "held")
			worker := bot("worker")
			if _, err := s.SessionClaim(bg, "default", se.ID, ts[0].ID, worker, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FinishAny(bg, "default", ts[0].ID, outcome, worker); !errors.Is(err, ErrNotReady) {
				t.Fatalf("finished a paused session: %v", err)
			}
			tk, _, err := s.Get(bg, "default", ts[0].ID)
			if err != nil || tk.Status != StatusInProgress || tk.Claim == nil {
				t.Fatalf("refusal changed task: %+v %v", tk, err)
			}
			if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
				t.Fatal(err)
			}
			if _, err := s.FinishAny(bg, "default", ts[0].ID, outcome, worker); err != nil {
				t.Fatalf("finish after resume: %v", err)
			}
		})
	}
}
