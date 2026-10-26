package tasks

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func ptr[T any](v T) *T { return &v }

// running makes a session that runs with the tasks named, in order, each depending on the
// ones at the indexes given.
func runningWith(t *testing.T, s *Store, titles ...string) (Session, []Task) {
	t.Helper()
	se := mustSession(t, s, "default", "work")
	var ts []Task
	for _, title := range titles {
		ts = append(ts, mustTask(t, s, "default", se.ID, title))
	}
	return mustRun(t, s, "default", se), ts
}

func claimNext(t *testing.T, s *Store, sid int64, who string) *Task {
	t.Helper()
	tk, err := s.SessionNext(bg, "default", sid, bot(who), true, 0)
	if err != nil {
		t.Fatalf("next --claim as %s: %v", who, err)
	}
	return tk
}

func finishTask(t *testing.T, s *Store, sid, id int64, who string) Task {
	t.Helper()
	tk, err := s.SessionFinish(bg, "default", sid, id, Outcome{Result: "did it"}, bot(who))
	if err != nil {
		t.Fatalf("finish #%d: %v", id, err)
	}
	return tk
}

func TestSessionNextRespectsDependencies(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "deps")
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	c := mustTask(t, s, "default", se.ID, "c", a.ID, b.ID)
	mustRun(t, s, "default", se)

	if got := claimNext(t, s, se.ID, "w1"); got == nil || got.ID != a.ID {
		t.Fatalf("first is a: %+v", got)
	}
	// a is held, b and c wait for it: nothing else is offered, to a look or a claim
	if got, _ := s.SessionNext(bg, "default", se.ID, bot("w2"), false, 0); got != nil {
		t.Fatalf("b waits for a, got %+v", got)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, b.ID, bot("w2"), 0); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a claim of b by id: %v", err)
	}
	if tk, _, _ := s.Get(bg, "default", b.ID); tk.Status != StatusReady || tk.Claim != nil {
		t.Fatalf("the refused claim changed b: %+v", tk)
	}
	// finishing a is not enough: Review is not done
	finishTask(t, s, se.ID, a.ID, "w1")
	if got, _ := s.SessionNext(bg, "default", se.ID, bot("w2"), false, 0); got != nil {
		t.Fatalf("b waits for a to be DONE, got %+v", got)
	}
	if _, err := s.Move(bg, "default", a.ID, StatusDone, Placement{}, human); err != nil {
		t.Fatal(err)
	}
	if got := claimNext(t, s, se.ID, "w2"); got == nil || got.ID != b.ID {
		t.Fatalf("then b: %+v", got)
	}
	finishTask(t, s, se.ID, b.ID, "w2")
	if _, err := s.Move(bg, "default", b.ID, StatusDone, Placement{}, human); err != nil {
		t.Fatal(err)
	}
	if got := claimNext(t, s, se.ID, "w3"); got == nil || got.ID != c.ID || len(got.DependsOn) != 2 {
		t.Fatalf("then c: %+v", got)
	}
}

func TestSessionNextSkipsEpicsAndBlocked(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "k")
	epic, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "epic", Acceptance: "x", Kind: KindEpic}, human)
	if err != nil {
		t.Fatal(err)
	}
	work := mustTask(t, s, "default", se.ID, "work")
	mustRun(t, s, "default", se)
	if got := claimNext(t, s, se.ID, "w"); got == nil || got.ID != work.ID {
		t.Fatalf("the epic is never handed out: %+v", got)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, epic.ID, bot("w"), 0); !errors.Is(err, ErrNotReady) {
		t.Fatalf("claim of an epic: %v", err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = 'ready', blocked = 1, claimed_by = '', claim_until = '' WHERE id = ?`, work.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SessionNext(bg, "default", se.ID, bot("w"), false, 0); got != nil {
		t.Fatalf("a blocked task is not handed out: %+v", got)
	}
}

func TestSessionDependencyCycleRejected(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "cyc")
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	c := mustTask(t, s, "default", se.ID, "c", b.ID)
	for _, tc := range []struct{ task, on int64 }{{a.ID, c.ID}, {a.ID, b.ID}, {b.ID, c.ID}, {a.ID, a.ID}} {
		if _, err := s.AddDependencies(bg, "default", se.ID, tc.task, []int64{tc.on}, human); !errors.Is(err, ErrInvalid) {
			t.Errorf("#%d on #%d: %v", tc.task, tc.on, err)
		}
	}
	if n := countWhere(t, db, "task_deps", "1=1"); n != 2 {
		t.Fatalf("a refused dependency left %d rows (want the 2 of the plan)", n)
	}
	// a legal one goes in, and is idempotent
	d := mustTask(t, s, "default", se.ID, "d")
	for i := 0; i < 2; i++ {
		got, err := s.AddDependencies(bg, "default", se.ID, d.ID, []int64{a.ID, c.ID}, bot("lead"))
		if err != nil || len(got.DependsOn) != 2 {
			t.Fatalf("legal dependency: %+v %v", got, err)
		}
	}
	// the operator removes one; an agent cannot
	if got, err := s.RemoveDependencies(bg, "default", se.ID, d.ID, []int64{c.ID}, human); err != nil || len(got.DependsOn) != 1 || got.DependsOn[0] != a.ID {
		t.Fatalf("remove: %+v %v", got, err)
	}
}

func TestSessionSplitKeepsDependencies(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "split")
	base := mustTask(t, s, "default", se.ID, "base")
	big := mustTask(t, s, "default", se.ID, "big", base.ID)
	after := mustTask(t, s, "default", se.ID, "after", big.ID)
	mustRun(t, s, "default", se)

	parts, err := s.SplitTask(bg, "default", se.ID, big.ID, []SplitPart{{Title: "big 1", Acceptance: "part one passes"}, {Title: "big 2", Acceptance: "part two passes"}}, bot("lead"))
	if err != nil || len(parts) != 2 {
		t.Fatalf("split: %+v %v", parts, err)
	}
	for _, p := range parts {
		if len(p.DependsOn) != 1 || p.DependsOn[0] != base.ID || p.Status != StatusReady || p.Acceptance == "" {
			t.Fatalf("a part keeps the original's dependencies: %+v", p)
		}
	}
	old, _, _ := s.Get(bg, "default", big.ID)
	if old.Status != StatusArchived {
		t.Fatalf("the original is archived, is %s", old.Status)
	}
	plan, _ := s.PlanOfSession(bg, "default", se.ID, human)
	var gotAfter Task
	for _, tk := range plan.Tasks {
		if tk.ID == after.ID {
			gotAfter = tk
		}
		if tk.ID == big.ID {
			t.Fatal("the archived original is still in the plan")
		}
	}
	if len(gotAfter.DependsOn) != 2 || gotAfter.DependsOn[0] != parts[0].ID || gotAfter.DependsOn[1] != parts[1].ID {
		t.Fatalf("what waited for the original waits for every part: %+v", gotAfter.DependsOn)
	}
	if plan.Session.Counts.Total != 4 {
		t.Fatalf("total after split: %+v", plan.Session.Counts)
	}
	// a task that has begun is not split; one part is not a split
	if got := claimNext(t, s, se.ID, "w"); got == nil || got.ID != base.ID {
		t.Fatalf("base first: %+v", got)
	}
	if _, err := s.SplitTask(bg, "default", se.ID, base.ID, []SplitPart{{Title: "a", Acceptance: "b"}, {Title: "c", Acceptance: "d"}}, bot("lead")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("splitting a held task: %v", err)
	}
	if _, err := s.SplitTask(bg, "default", se.ID, after.ID, []SplitPart{{Title: "only", Acceptance: "one"}}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("one part: %v", err)
	}
	if _, err := s.SplitTask(bg, "default", se.ID, after.ID, []SplitPart{{Title: "a"}, {Title: "c", Acceptance: "d"}}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a part with no criteria: %v", err)
	}
}

func TestSessionSplitCountsAgainstTheCap(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "cap")
	a := mustTask(t, s, "default", se.ID, "a")
	mustTask(t, s, "default", se.ID, "b")
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{MaxTasks: ptr(3)}, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SplitTask(bg, "default", se.ID, a.ID, []SplitPart{{Title: "1", Acceptance: "x"}, {Title: "2", Acceptance: "x"}, {Title: "3", Acceptance: "x"}}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("a split past the cap: %v", err)
	}
	if n := countWhere(t, db, "tasks", "session_id = ? AND status <> 'archived'", se.ID); n != 2 {
		t.Fatalf("a refused split changed %d tasks", n)
	}
}

func TestSessionRetryCapGoesToBlocked(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "flaky")
	id := ts[0].ID
	for fail := 1; fail <= 2; fail++ {
		if got := claimNext(t, s, se.ID, "worker"); got == nil || got.ID != id {
			t.Fatalf("attempt %d: %+v", fail, got)
		}
		finishTask(t, s, se.ID, id, "worker")
		tk, err := s.VerifySessionTask(bg, "default", se.ID, id, false, "test X still fails", bot("checker"))
		if err != nil || tk.Status != StatusReady || tk.Retries != fail || tk.Blocked {
			t.Fatalf("failure %d returns to Ready: %+v %v", fail, tk, err)
		}
		_, evs, _ := s.Get(bg, "default", id)
		if last := evs[len(evs)-1]; last.Kind != "verify_failed" || last.Note != "test X still fails" {
			t.Fatalf("the note travels with the task: %+v", last)
		}
	}
	claimNext(t, s, se.ID, "worker")
	finishTask(t, s, se.ID, id, "worker")
	tk, err := s.VerifySessionTask(bg, "default", se.ID, id, false, "third strike", bot("checker"))
	if err != nil || !tk.Blocked || tk.Status != StatusReview || tk.Retries != 3 {
		t.Fatalf("the third failure blocks: %+v %v", tk, err)
	}
	if got, _ := s.SessionNext(bg, "default", se.ID, bot("worker"), true, 0); got != nil {
		t.Fatalf("a blocked task is not handed out: %+v", got)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, id, true, "", bot("checker")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a blocked task is not verified: %v", err)
	}
	if got, _ := s.GetSession(bg, "default", se.ID, human); got.Counts.Blocked != 1 || got.Counts.Review != 1 {
		t.Fatalf("counts: %+v", got.Counts)
	}
	// the operator puts it back: the flag and the retries are cleared
	back, err := s.Move(bg, "default", id, StatusReady, Placement{}, human)
	if err != nil || back.Blocked || back.Retries != 0 {
		t.Fatalf("operator unblocks: %+v %v", back, err)
	}
	if got := claimNext(t, s, se.ID, "worker"); got == nil || got.ID != id {
		t.Fatalf("handed out again: %+v", got)
	}
}

func TestSessionRetryCapFollowsTheSessionSetting(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "once")
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{MaxRetries: ptr(0)}, human); err != nil {
		t.Fatal(err)
	}
	claimNext(t, s, se.ID, "w")
	finishTask(t, s, se.ID, ts[0].ID, "w")
	tk, err := s.VerifySessionTask(bg, "default", se.ID, ts[0].ID, false, "no", bot("c"))
	if err != nil || !tk.Blocked {
		t.Fatalf("no retries allowed: %+v %v", tk, err)
	}
}

func TestSessionVerifyPassDoesNotCloseUnlessAutoClose(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, auto := range []bool{false, true} {
		se := mustSession(t, s, "default", "v")
		tk := mustTask(t, s, "default", se.ID, "t")
		if auto {
			if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{AutoCloseVerified: ptr(true)}, human); err != nil {
				t.Fatal(err)
			}
		}
		mustRun(t, s, "default", se)
		claimNext(t, s, se.ID, "worker")
		finishTask(t, s, se.ID, tk.ID, "worker")
		got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "looks right", bot("checker"))
		if err != nil || !got.Verified {
			t.Fatalf("auto=%v verify: %+v %v", auto, got, err)
		}
		want := StatusReview
		if auto {
			want = StatusDone
		}
		if got.Status != want {
			t.Errorf("auto_close_verified=%v: a verified task is in %s, want %s", auto, got.Status, want)
		}
		if !auto {
			closed, err := s.Move(bg, "default", tk.ID, StatusDone, Placement{}, human)
			if err != nil || closed.Status != StatusDone || !closed.Verified {
				t.Fatalf("the operator closes it: %+v %v", closed, err)
			}
		}
	}
}

func TestSessionVerifyRules(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "t")
	id := ts[0].ID
	if _, err := s.VerifySessionTask(bg, "default", se.ID, id, true, "", bot("c")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a task not in Review: %v", err)
	}
	claimNext(t, s, se.ID, "worker")
	finishTask(t, s, se.ID, id, "worker")
	if _, err := s.VerifySessionTask(bg, "default", se.ID, id, true, "", bot("worker")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the worker verifies its own work: %v", err)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, id, false, " ", bot("c")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a failure needs a note: %v", err)
	}
	if tk, _, _ := s.Get(bg, "default", id); tk.Verified || tk.Status != StatusReview || tk.Retries != 0 {
		t.Fatalf("refused verdicts changed the task: %+v", tk)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, id, true, "ok", bot("c")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, id, true, "ok", bot("c2")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("verified twice: %v", err)
	}
	// a question is the operator's to answer
	q := mustTask(t, s, "default", se.ID, "q")
	claimNext(t, s, se.ID, "worker")
	if _, err := s.SessionFinish(bg, "default", se.ID, q.ID, Outcome{Question: "which?"}, bot("worker")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, q.ID, true, "ok", bot("c")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("verifying a question: %v", err)
	}
}

func TestSessionStopReleasesAllClaims(t *testing.T) {
	s, db, c := newTestStore(t)
	se, ts := runningWith(t, s, "a", "b", "c")
	claimNext(t, s, se.ID, "w1")
	claimNext(t, s, se.ID, "w2")
	if n := countWhere(t, db, "tasks", "session_id = ? AND claimed_by <> ''", se.ID); n != 2 {
		t.Fatalf("setup: %d claims", n)
	}
	// a claim on a personal task is not the session's to release
	mine := mustAdd(t, s, "default", "mine", true)
	if _, err := s.Claim(bg, "default", mine.ID, bot("w3"), 0); err != nil {
		t.Fatal(err)
	}
	got, err := s.StopSession(bg, "default", se.ID, human)
	if err != nil || got.State != SessionStopped {
		t.Fatalf("stop: %+v %v", got, err)
	}
	if n := countWhere(t, db, "tasks", "session_id = ? AND claimed_by <> ''", se.ID); n != 0 {
		t.Fatalf("%d claims survived the stop", n)
	}
	if n := countWhere(t, db, "tasks", "session_id = ? AND status = 'ready'", se.ID); n != 3 {
		t.Fatalf("tasks stay in place, released ones in Ready: %d ready", n)
	}
	if n := countWhere(t, db, "tasks", "id = ? AND claimed_by = 'w3'", mine.ID); n != 1 {
		t.Fatal("the stop released a personal claim")
	}
	c.advance(time.Hour)
	if tk, _ := s.SessionNext(bg, "default", se.ID, bot("w1"), true, 0); tk != nil {
		t.Fatal("a stopped session hands out tasks")
	}
	// the released holder can no longer finish: the session is stopped
	if _, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "x"}, bot("w1")); !errors.Is(err, ErrNotReady) {
		t.Fatalf("finish after stop: %v", err)
	}
}

func TestSessionPausedKeepsClaimsAndHoldersCommentButDoNotFinish(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "a")
	claimNext(t, s, se.ID, "w")
	if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "done"}, bot("w")); !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("the holder finishes in a paused session: %v", err)
	}
	if tk, _, _ := s.Get(bg, "default", ts[0].ID); tk.Status != StatusInProgress || tk.Claim == nil {
		t.Fatalf("the refused finish changed the task: %+v", tk)
	}
	if _, err := s.SessionComment(bg, "default", se.ID, ts[0].ID, "still on it", bot("w")); err != nil {
		t.Fatalf("a comment stays allowed: %v", err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, ts[0].ID, bot("w2"), 0); !errors.Is(err, ErrNotReady) {
		t.Fatalf("claim in a paused session: %v", err)
	}
	if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if tk, err := s.SessionFinish(bg, "default", se.ID, ts[0].ID, Outcome{Result: "done"}, bot("w")); err != nil || tk.Status != StatusReview {
		t.Fatalf("after the resume: %+v %v", tk, err)
	}
}

func TestSessionArtifactsRules(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, ts := runningWith(t, s, "a")
	id := ts[0].ID
	if _, err := s.AddArtifact(bg, "default", se.ID, id, ArtifactInput{Kind: "commit", Ref: "abc123"}, bot("w")); !errors.Is(err, ErrNotClaimant) {
		t.Fatalf("an agent that does not hold the task: %v", err)
	}
	claimNext(t, s, se.ID, "w")
	a, err := s.AddArtifact(bg, "default", se.ID, id, ArtifactInput{Kind: "commit", Ref: "abc123", Note: "the fix"}, bot("w"))
	if err != nil || a.Ref != "abc123" || a.Actor != "w" {
		t.Fatalf("artifact: %+v %v", a, err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, id, ArtifactInput{Kind: "weird", Ref: "x"}, bot("w")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("kind: %v", err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, id, ArtifactInput{Kind: "file"}, bot("w")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ref: %v", err)
	}
	d, err := s.SessionTask(bg, "default", se.ID, id, human)
	if err != nil || len(d.Artifacts) != 1 || d.Artifacts[0].Kind != "commit" || len(d.Events) == 0 {
		t.Fatalf("detail: %+v %v", d, err)
	}
}

func TestPersonalBoardBehaviourIsUnchanged(t *testing.T) {
	s, db, _ := newTestStore(t)
	// a session exists beside the board, and must change nothing of it
	se := mustSession(t, s, "default", "beside")
	mustTask(t, s, "default", se.ID, "session work")
	mustRun(t, s, "default", se)

	in := mustAdd(t, s, "default", "inbox one", false)
	if in.SessionID != 0 || in.Kind != "" || in.DependsOn != nil || in.Acceptance != "" {
		t.Fatalf("a personal task carries session fields: %+v", in)
	}
	r := mustAdd(t, s, "default", "ready one", true)
	got, err := s.Next(bg, "default", bot("w"), true, 0)
	if err != nil || got == nil || got.ID != r.ID {
		t.Fatalf("personal next: %+v %v", got, err)
	}
	if _, err := s.Finish(bg, "default", r.ID, Outcome{Result: "ok"}, bot("w")); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Counts(bg, "default"); c.Inbox != 1 || c.Review != 1 || c.Ready != 0 {
		t.Fatalf("counts: %+v", c)
	}
	// the open-task limit is the personal board's: session tasks do not count against it
	if n := countWhere(t, db, "tasks", "session_id IS NULL"); n != 2 {
		t.Fatalf("personal rows: %d", n)
	}
	var open int
	if err := db.QueryRow(openTasksSQL, "default").Scan(&open); err != nil || open != 2 {
		t.Fatalf("open tasks counted for the limit: %d %v", open, err)
	}
	// personal approve/move still work and a personal task ignores dependencies
	if _, err := s.Approve(bg, "default", []int64{in.ID}, false, human); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Next(bg, "default", bot("w"), false, 0); n == nil || n.ID != in.ID {
		t.Fatalf("approved personal task is next: %+v", n)
	}
	if r, _ := s.Rev(bg, "default"); r == 0 {
		t.Fatal("personal revision")
	}
}
