package tasks

import (
	"errors"
	"testing"
)

// The session tests follow the "revert proof" rule: each asserts state (rows, columns,
// revisions), not only a refusal's words, so that undoing the rule it covers fails it.

func mustSession(t *testing.T, s *Store, profile, title string) Session {
	t.Helper()
	se, err := s.CreateSession(bg, profile, SessionInput{Title: title, Goal: "goal of " + title}, human)
	if err != nil {
		t.Fatalf("create session %q: %v", title, err)
	}
	return se
}

func mustTask(t *testing.T, s *Store, profile string, session int64, title string, deps ...int64) Task {
	t.Helper()
	tk, err := s.AddSessionTask(bg, profile, session, SessionTaskInput{Title: title, Acceptance: "done when " + title, DependsOn: deps}, human)
	if err != nil {
		t.Fatalf("add %q: %v", title, err)
	}
	return tk
}

func mustRun(t *testing.T, s *Store, profile string, se Session) Session {
	t.Helper()
	out, err := s.ApproveSession(bg, profile, se.ID, human)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	return out
}

func TestSessionCreateDefaultsAndDraftState(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "Ship it")
	if se.State != SessionDraft || se.MaxTasks != 200 || se.MaxDepth != 3 || se.MaxRetries != 2 || se.AccessLevel != "scoped" || se.AutoCloseVerified {
		t.Fatalf("defaults: %+v", se)
	}
	if se.CreatedBy != "you" || se.ApprovedBy != "" || se.ApprovedAt != nil {
		t.Fatalf("authorship: %+v", se)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: " "}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a session needs a title: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", AccessLevel: "root"}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown access: %v", err)
	}
}

func TestSessionAgentCreatesDraftButCannotSetOperatorFields(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "plan", Goal: "g"}, bot("lead"))
	if err != nil || se.State != SessionDraft || se.CreatedBy != "lead" {
		t.Fatalf("agent draft: %+v %v", se, err)
	}
	for _, in := range []SessionInput{{Title: "x", BudgetUSD: 5}, {Title: "x", MaxTasks: 9999}, {Title: "x", AccessLevel: "full"}, {Title: "x", MaxDepth: 9}} {
		if _, err := s.CreateSession(bg, "default", in, bot("lead")); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("agent set %+v: %v", in, err)
		}
	}
	if n := countWhere(t, db, "task_sessions", "1=1"); n != 1 {
		t.Fatalf("a refused create left %d sessions", n)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x"}, Actor{Kind: Capture, Name: "os"}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("capture: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x"}, Actor{Kind: Agent, Name: "w", Session: se.ID}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a bound agent starts no session: %v", err)
	}
}

// Every verb that only the operator may do refuses an agent, and changes nothing.
func TestSessionOperatorOnlyMatrix(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	tk := mustTask(t, s, "default", se.ID, "t")
	running := mustSession(t, s, "default", "r")
	mustTask(t, s, "default", running.ID, "rt")
	mustRun(t, s, "default", running)
	budget, auto, level := 9.0, true, "full"
	agent := bot("lead")
	verbs := map[string]func() error{
		"approve": func() error { _, err := s.ApproveSession(bg, "default", se.ID, agent); return err },
		"resume":  func() error { _, err := s.ResumeSession(bg, "default", running.ID, agent); return err },
		"close": func() error {
			_, err := s.CloseSession(bg, "default", running.ID, SessionDone, "", agent)
			return err
		},
		"archive": func() error { _, err := s.ArchiveSession(bg, "default", se.ID, agent); return err },
		"budget": func() error {
			_, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &budget}, agent)
			return err
		},
		"auto-close": func() error {
			_, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{AutoCloseVerified: &auto}, agent)
			return err
		},
		"access": func() error {
			_, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{AccessLevel: &level}, agent)
			return err
		},
		"spend": func() error { _, err := s.AddSessionSpend(bg, "default", se.ID, 1, "", agent); return err },
		"undepend": func() error {
			_, err := s.RemoveDependencies(bg, "default", se.ID, tk.ID, []int64{1}, agent)
			return err
		},
		"capture-p": func() error { _, err := s.PauseSession(bg, "default", running.ID, Actor{Kind: Capture}); return err },
	}
	for name, fn := range verbs {
		if err := fn(); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%s by an agent: %v", name, err)
		}
	}
	got, _ := s.GetSession(bg, "default", se.ID, human)
	if got.State != SessionDraft || got.BudgetUSD != 0 || got.AutoCloseVerified || got.AccessLevel != "scoped" || got.SpentUSD != 0 || got.Archived {
		t.Fatalf("a refused verb changed the session: %+v", got)
	}
	if r, _ := s.GetSession(bg, "default", running.ID, human); r.State != SessionRunning {
		t.Fatalf("the running session moved: %v", r.State)
	}
	if n := countWhere(t, db, "tasks", "id = ? AND status = 'inbox'", tk.ID); n != 1 {
		t.Fatal("a refused approve moved a task")
	}
}

func TestSessionApproveMovesDraftToReadyAndRecordsWho(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	if _, err := s.ApproveSession(bg, "default", se.ID, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty plan is not approved: %v", err)
	}
	a := mustTask(t, s, "default", se.ID, "a")
	b := mustTask(t, s, "default", se.ID, "b", a.ID)
	if a.Status != StatusInbox || b.Status != StatusInbox {
		t.Fatalf("draft tasks wait in the inbox column: %s %s", a.Status, b.Status)
	}
	got := mustRun(t, s, "default", se)
	if got.State != SessionRunning || got.ApprovedBy != "you" || got.ApprovedAt == nil {
		t.Fatalf("approved: %+v", got)
	}
	if got.Counts.Ready != 2 || got.Counts.Inbox != 0 {
		t.Fatalf("counts after approval: %+v", got.Counts)
	}
	if _, err := s.ApproveSession(bg, "default", se.ID, human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("approving twice: %v", err)
	}
}

func TestSessionDraftTasksAreInvisibleToNextAndPersonalLists(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	mustTask(t, s, "default", se.ID, "draft work")
	// not handed out by the session while it is a draft, not even for the operator's claim
	if tk, err := s.SessionNext(bg, "default", se.ID, bot("w"), false, 0); err != nil || tk != nil {
		t.Fatalf("a draft hands out nothing: %v %v", tk, err)
	}
	if tk, err := s.SessionNext(bg, "default", se.ID, bot("w"), true, 0); err != nil || tk != nil {
		t.Fatalf("a draft is not claimed: %v %v", tk, err)
	}
	if n := countWhere(t, db, "tasks", "claimed_by <> ''"); n != 0 {
		t.Fatal("a draft task was claimed")
	}
	// even forced to Ready by hand (the operator drags), a draft's task is not taken
	if _, err := db.Exec(`UPDATE tasks SET status = 'ready'`); err != nil {
		t.Fatal(err)
	}
	if tk, _ := s.SessionNext(bg, "default", se.ID, bot("w"), true, 0); tk != nil {
		t.Fatal("a ready task of a draft was claimed")
	}
	// the personal board never shows it
	for _, a := range []Actor{human, bot("w")} {
		if ts, err := s.List(bg, "default", Filter{Statuses: []Status{StatusInbox, StatusReady}}, a); err != nil || len(ts) != 0 {
			t.Fatalf("personal list as %v: %v %v", a, ts, err)
		}
		if tk, err := s.Next(bg, "default", a, false, 0); err != nil || tk != nil {
			t.Fatalf("personal next: %v %v", tk, err)
		}
	}
	b, err := s.Board(bg, "default", 0, human)
	if err != nil || b.Counts.Inbox+b.Counts.Ready != 0 || len(b.Tasks[StatusInbox])+len(b.Tasks[StatusReady]) != 0 {
		t.Fatalf("personal board: %+v %v", b.Counts, err)
	}
	if c, _ := s.Counts(bg, "default"); c != (Counts{}) {
		t.Fatalf("personal counts: %+v", c)
	}
}

func TestSessionIsolationBetweenSessionsAndPersonalBoard(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustSession(t, s, "default", "A"), mustSession(t, s, "default", "B")
	ta := mustTask(t, s, "default", a.ID, "in A")
	tb := mustTask(t, s, "default", b.ID, "in B")
	personal := mustAdd(t, s, "default", "mine", true)
	mustRun(t, s, "default", a)
	mustRun(t, s, "default", b)

	ts, err := s.SessionTasks(bg, "default", a.ID, Filter{}, human)
	if err != nil || len(ts) != 1 || ts[0].ID != ta.ID {
		t.Fatalf("session A lists %+v %v", ts, err)
	}
	if ts, _ = s.SessionTasks(bg, "default", b.ID, Filter{}, human); len(ts) != 1 || ts[0].ID != tb.ID {
		t.Fatalf("session B lists %+v", ts)
	}
	ps, _ := s.List(bg, "default", Filter{}, human)
	if len(ps) != 1 || ps[0].ID != personal.ID {
		t.Fatalf("personal list holds %+v", ps)
	}
	// next of A is A's task; the personal next is the personal one
	if n, _ := s.SessionNext(bg, "default", a.ID, bot("w"), false, 0); n == nil || n.ID != ta.ID {
		t.Fatalf("next of A: %+v", n)
	}
	if n, _ := s.Next(bg, "default", bot("w"), false, 0); n == nil || n.ID != personal.ID {
		t.Fatalf("personal next: %+v", n)
	}
	// B's task by id from A's session, and from the personal commands, is refused
	if _, err := s.SessionClaim(bg, "default", a.ID, tb.ID, bot("w"), 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim B's task through A: %v", err)
	}
	if _, err := s.Claim(bg, "default", ta.ID, bot("w"), 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a personal claim of a session task: %v", err)
	}
	if _, err := s.SessionTask(bg, "default", a.ID, tb.ID, human); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read B's task through A: %v", err)
	}
	if tk, _, _ := s.Get(bg, "default", ta.ID); tk.Status != StatusReady {
		t.Fatalf("the refused claims changed A's task: %s", tk.Status)
	}
	// a dependency across sessions is refused
	if _, err := s.AddSessionTask(bg, "default", a.ID, SessionTaskInput{Title: "x", Acceptance: "y", DependsOn: []int64{tb.ID}}, human); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a cross-session dependency: %v", err)
	}
	// counts per board do not mix
	if c, _ := s.Counts(bg, "default"); c.Ready != 1 {
		t.Fatalf("personal counts: %+v", c)
	}
}

func TestSessionBoundAgentActsOnlyInItsSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustSession(t, s, "default", "A"), mustSession(t, s, "default", "B")
	ta := mustTask(t, s, "default", a.ID, "in A")
	tb := mustTask(t, s, "default", b.ID, "in B")
	mustRun(t, s, "default", a)
	mustRun(t, s, "default", b)
	w := Actor{Kind: Agent, Name: "worker", Session: a.ID}
	if _, err := s.SessionClaim(bg, "default", b.ID, tb.ID, w, 0); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("claim in the other session: %v", err)
	}
	if _, err := s.SessionNext(bg, "default", b.ID, w, true, 0); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("next in the other session: %v", err)
	}
	if _, err := s.GetSession(bg, "default", b.ID, w); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read the other session: %v", err)
	}
	if _, _, err := s.Add(bg, "default", AddInput{Title: "x"}, w); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a bound agent adds to the personal board: %v", err)
	}
	if _, err := s.List(bg, "default", Filter{}, w); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a bound agent lists the personal board: %v", err)
	}
	if _, err := s.PauseSession(bg, "default", b.ID, w); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause the other session: %v", err)
	}
	if got, err := s.SessionClaim(bg, "default", a.ID, ta.ID, w, 0); err != nil || got.Claim == nil || got.Claim.By != "worker" {
		t.Fatalf("claim in its own session: %+v %v", got, err)
	}
	if ss, _ := s.ListSessions(bg, "default", SessionFilter{}, w); len(ss) != 1 || ss[0].ID != a.ID {
		t.Fatalf("a bound agent lists %+v", ss)
	}
	if got, _ := s.GetSession(bg, "default", b.ID, human); got.State != SessionRunning {
		t.Fatal("the other session was paused")
	}
}

func TestSessionTaskCapAndAcceptance(t *testing.T) {
	s, db, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	limit2 := 2
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{MaxTasks: &limit2}, human); err != nil {
		t.Fatal(err)
	}
	mustTask(t, s, "default", se.ID, "one")
	mustTask(t, s, "default", se.ID, "two")
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "three", Acceptance: "x"}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("over the cap: %v", err)
	}
	if n := countWhere(t, db, "tasks", "session_id = ?", se.ID); n != 2 {
		t.Fatalf("a refused add left %d tasks", n)
	}
	mustRun(t, s, "default", se)
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "four", Acceptance: "x"}, bot("lead")); !errors.Is(err, ErrLimit) {
		t.Fatalf("an agent over the cap after approval: %v", err)
	}
	// an operator raise is honoured; additions after approval land in Ready at once
	limit5 := 5
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{MaxTasks: &limit5}, human); err != nil {
		t.Fatal(err)
	}
	tk, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "follow-up", Acceptance: "x"}, bot("lead"))
	if err != nil || tk.Status != StatusReady || tk.Author != "lead" {
		t.Fatalf("follow-up: %+v %v", tk, err)
	}
	for _, in := range []SessionTaskInput{{Title: "no criteria"}, {Title: "blank", Acceptance: " \n "}} {
		if _, err := s.AddSessionTask(bg, "default", se.ID, in, human); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	// a cap below the tasks already there is a plain cap: nothing more fits, nothing is lost
	if n := countWhere(t, db, "tasks", "session_id = ?", se.ID); n != 3 {
		t.Fatalf("tasks: %d", n)
	}
}

func TestSessionDepthCap(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	epic, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "epic", Acceptance: "x", Kind: KindEpic}, human)
	if err != nil {
		t.Fatal(err)
	}
	mid, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "mid", Acceptance: "x", ParentID: epic.ID}, human)
	if err != nil || mid.ParentID != epic.ID {
		t.Fatalf("mid: %+v %v", mid, err)
	}
	leaf, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "leaf", Acceptance: "x", ParentID: mid.ID}, human)
	if err != nil {
		t.Fatalf("depth 3 is allowed: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "deeper", Acceptance: "x", ParentID: leaf.ID}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("depth 4: %v", err)
	}
}

func TestSessionCascadeDeleteWithProfile(t *testing.T) {
	s, db, _ := newTestStore(t)
	p := addProfile(t, db, "gone")
	se := mustSession(t, s, p, "s")
	a := mustTask(t, s, p, se.ID, "a")
	b := mustTask(t, s, p, se.ID, "b", a.ID)
	mustRun(t, s, p, se)
	if _, err := s.AddArtifact(bg, p, se.ID, b.ID, ArtifactInput{Kind: "file", Ref: "x.go"}, human); err != nil {
		t.Fatal(err)
	}
	keep := mustSession(t, s, "default", "keep")
	mustTask(t, s, "default", keep.ID, "kept")
	for _, table := range []string{"task_sessions", "tasks", "task_deps", "task_artifacts", "task_session_events", "task_session_rev", "task_events"} {
		if countWhere(t, db, table, "1=1") == 0 {
			t.Fatalf("setup: %s is empty", table)
		}
	}
	if _, err := db.Exec(`DELETE FROM profiles WHERE id = ?`, p); err != nil {
		t.Fatal(err)
	}
	if n := countWhere(t, db, "task_sessions", "profile_id = ?", p); n != 0 {
		t.Fatalf("%d sessions survived the profile", n)
	}
	if n := countWhere(t, db, "tasks", "session_id = ?", se.ID); n != 0 {
		t.Fatalf("%d session tasks survived", n)
	}
	for table, where := range map[string]string{
		"task_deps":           "task_id IN (" + itoa(a.ID) + "," + itoa(b.ID) + ")",
		"task_artifacts":      "task_id = " + itoa(b.ID),
		"task_session_events": "session_id = " + itoa(se.ID),
		"task_session_rev":    "session_id = " + itoa(se.ID),
	} {
		if n := countWhere(t, db, table, where); n != 0 {
			t.Errorf("%d rows of %s survived", n, table)
		}
	}
	if n := countWhere(t, db, "tasks", "session_id = ?", keep.ID); n != 1 {
		t.Fatal("another profile's session lost its task")
	}
}

func TestSessionBoardRevBumpsPerSessionAndNotThePersonalBoard(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustSession(t, s, "default", "A"), mustSession(t, s, "default", "B")
	rev := func(id int64) int64 {
		r, err := s.SessionRev(bg, id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	personal0, _ := s.Rev(bg, "default")
	a0, b0 := rev(a.ID), rev(b.ID)
	tk := mustTask(t, s, "default", a.ID, "t")
	if rev(a.ID) <= a0 || rev(b.ID) != b0 {
		t.Fatalf("adding to A: A %d->%d, B %d->%d", a0, rev(a.ID), b0, rev(b.ID))
	}
	if p, _ := s.Rev(bg, "default"); p != personal0 {
		t.Fatalf("a session write moved the personal revision: %d -> %d", personal0, p)
	}
	a1 := rev(a.ID)
	mustRun(t, s, "default", a)
	a2 := rev(a.ID)
	if a2 <= a1 {
		t.Fatal("approve did not bump")
	}
	if _, err := s.SessionClaim(bg, "default", a.ID, tk.ID, bot("w"), 0); err != nil || rev(a.ID) <= a2 {
		t.Fatalf("claim did not bump: %v", err)
	}
	a3 := rev(a.ID)
	// the operator's personal verbs on a session task move that session's revision too
	if _, err := s.Edit(bg, "default", tk.ID, Edit{Title: ptr("renamed")}, human); err != nil || rev(a.ID) <= a3 {
		t.Fatalf("edit by the operator: %v", err)
	}
	a4 := rev(a.ID)
	if _, err := s.Move(bg, "default", tk.ID, StatusReview, Placement{}, human); err != nil || rev(a.ID) <= a4 {
		t.Fatalf("move by the operator: %v", err)
	}
	if rev(b.ID) != b0 {
		t.Fatal("B moved")
	}
	if got, _ := s.GetSession(bg, "default", a.ID, human); got.Rev != rev(a.ID) {
		t.Fatalf("session rev %d vs %d", got.Rev, rev(a.ID))
	}
}
