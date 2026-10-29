package tasks

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// runningChild is a child of parent with one task, approved so that it runs.
func runningChild(t *testing.T, s *Store, parent int64, title string) Session {
	t.Helper()
	c := child(t, s, "default", parent, title)
	mustTask(t, s, "default", c.ID, title+" task")
	return mustRun(t, s, "default", c)
}

func stateAndArchived(t *testing.T, s *Store, id int64) (SessionState, bool) {
	t.Helper()
	se, err := s.GetSession(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	return se.State, se.Archived
}

func eventNotes(t *testing.T, s *Store, id int64, kind string) []string {
	t.Helper()
	evs, err := s.SessionEvents(bg, "default", id, 0, human)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, e := range evs {
		if e.Kind == kind {
			notes = append(notes, e.Note)
		}
	}
	return notes
}

func TestArchiveRefusesWhileSessionsBelowAreOpen(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	draft := child(t, s, "default", root.ID, "the draft one")
	run := runningChild(t, s, root.ID, "the running\none")
	deep := child(t, s, "default", run.ID, "deep draft")
	_, err := s.ArchiveSession(bg, "default", root.ID, human)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("archive with open children: %v", err)
	}
	for _, want := range []string{"#" + itoa(draft.ID), "the draft one", "the running one", "#" + itoa(deep.ID), "--cascade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}
	for _, id := range []int64{root.ID, draft.ID, run.ID, deep.ID} {
		if _, arch := stateAndArchived(t, s, id); arch {
			t.Fatalf("session %d was archived by a refusal", id)
		}
	}
}

func TestArchiveRefusalNamesOnlyTenAndCountsTheRest(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	for i := 0; i < maxListedOpen+3; i++ {
		child(t, s, "default", root.ID, "c")
	}
	_, err := s.ArchiveSession(bg, "default", root.ID, human)
	if err == nil || !strings.Contains(err.Error(), "and 3 more") {
		t.Fatalf("refusal: %v", err)
	}
}

func TestArchiveTakesFinishedChildrenAlongWithoutAPrompt(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	done := runningChild(t, s, root.ID, "done one")
	if _, err := s.CloseSession(bg, "default", done.ID, SessionDone, "ok", human); err != nil {
		t.Fatal(err)
	}
	stopped := runningChild(t, s, root.ID, "stopped one")
	if _, err := s.StopSession(bg, "default", stopped.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveSession(bg, "default", root.ID, human); err != nil {
		t.Fatalf("archive: %v", err)
	}
	for _, id := range []int64{root.ID, done.ID, stopped.ID} {
		if _, arch := stateAndArchived(t, s, id); !arch {
			t.Errorf("session %d is not archived", id)
		}
	}
	if n := eventNotes(t, s, done.ID, "archived"); len(n) != 1 || !strings.Contains(n[0], "#"+itoa(root.ID)) {
		t.Errorf("done child event: %v", n)
	}
}

func TestCascadeArchivesTheOpenSubtreeInOneWriteDeepestFirst(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	run := runningChild(t, s, root.ID, "running")
	deep := runningChild(t, s, run.ID, "deep running")
	draft := child(t, s, "default", root.ID, "draft")
	done := runningChild(t, s, root.ID, "done")
	if _, err := s.CloseSession(bg, "default", done.ID, SessionDone, "ok", human); err != nil {
		t.Fatal(err)
	}
	// the deep session holds a claim
	if n, err := s.SessionNext(bg, "default", deep.ID, bot("w"), true, 0); err != nil || n == nil {
		t.Fatalf("claim: %v %v", n, err)
	}
	ids, err := s.ArchiveSessionCascade(bg, "default", root.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 5 || ids[len(ids)-1] != root.ID || ids[0] != deep.ID {
		t.Fatalf("ids = %v, want 5 with %d first and the root last", ids, deep.ID)
	}
	for _, id := range []int64{root.ID, run.ID, deep.ID, draft.ID, done.ID} {
		if _, arch := stateAndArchived(t, s, id); !arch {
			t.Errorf("session %d is not archived", id)
		}
		if n := eventNotes(t, s, id, "archived"); len(n) != 1 || !strings.Contains(n[0], "#"+itoa(root.ID)) && id != root.ID {
			t.Errorf("session %d archived events: %v", id, n)
		}
	}
	if st, _ := stateAndArchived(t, s, run.ID); st != SessionStopped {
		t.Errorf("a running child ends %s, want stopped", st)
	}
	if st, _ := stateAndArchived(t, s, done.ID); st != SessionDone {
		t.Errorf("a done child ends %s, want done", st)
	}
	var held int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE session_id = ? AND claimed_by <> ''`, deep.ID).Scan(&held); err != nil || held != 0 {
		t.Errorf("claims still held: %d %v", held, err)
	}
}

func TestCascadeOfAClosedSubtreeAndOfAllAlreadyArchivedAreHandled(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	ids, err := s.ArchiveSessionCascade(bg, "default", root.ID, human)
	if err != nil || !slices.Equal(ids, []int64{root.ID}) {
		t.Fatalf("a session with no children: %v %v", ids, err)
	}
	if _, err := s.ArchiveSessionCascade(bg, "default", root.ID, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archiving twice: %v", err)
	}
}

func TestCascadeIsTheOperatorsAndNeverAnAgents(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	run := runningChild(t, s, root.ID, "running")
	lead := Actor{Kind: Agent, Name: "lead", Session: root.ID}
	for _, a := range []Actor{bot("x"), lead, {Kind: Capture}} {
		if _, err := s.ArchiveSessionCascade(bg, "default", root.ID, a); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("cascade by %+v: %v", a, err)
		}
		if _, err := s.ArchiveSession(bg, "default", root.ID, a); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("archive by %+v: %v", a, err)
		}
	}
	for _, id := range []int64{root.ID, run.ID} {
		if st, arch := stateAndArchived(t, s, id); arch || st == SessionStopped {
			t.Fatalf("session %d touched by an agent: %s %v", id, st, arch)
		}
	}
}

func TestCascadeFailsClosedOnACycleAndOnATooDeepTree(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	a := child(t, s, "default", root.ID, "a")
	b := child(t, s, "default", a.ID, "b")
	if _, err := s.db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	for _, do := range []func() error{
		func() error { _, err := s.ArchiveSessionCascade(bg, "default", a.ID, human); return err },
		func() error { _, err := s.ArchiveSession(bg, "default", a.ID, human); return err },
	} {
		if err := do(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("a cycle: %v", err)
		}
	}
	// the parents of the cycle are put right; now a tree one level too deep
	if _, err := s.db.Exec(`UPDATE task_sessions SET parent_session_id = NULL WHERE id = ?`, root.ID); err != nil {
		t.Fatal(err)
	}
	cur := b
	for cur.Depth < HardMaxSessionDepth {
		cur = child(t, s, "default", cur.ID, "level")
	}
	extra := mustSession(t, s, "default", "extra")
	if _, err := s.db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, cur.ID, extra.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveSessionCascade(bg, "default", root.ID, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("a too deep tree: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE archived_at <> ''`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d sessions archived by a refused cascade (%v)", n, err)
	}
}

func TestCascadeDoesNotReturnTheChildsBudgetToTheParent(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	b := 10.0
	if _, err := s.UpdateSession(bg, "default", root.ID, SessionUpdate{BudgetUSD: &b}, human); err != nil {
		t.Fatal(err)
	}
	c := child(t, s, "default", root.ID, "c")
	before, _ := s.GetSession(bg, "default", root.ID, human)
	if _, err := s.ArchiveSessionCascade(bg, "default", root.ID, human); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetSession(bg, "default", root.ID, human)
	if after.BudgetUSD != before.BudgetUSD || after.SpentUSD != before.SpentUSD || c.ID == 0 {
		t.Fatalf("budget moved: %+v -> %+v", before, after)
	}
}


func TestArchiveRefusalCleansTheTitlesItQuotes(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	c := child(t, s, "default", root.ID, "plain")
	if _, err := s.db.Exec(`UPDATE task_sessions SET title = ? WHERE id = ?`, "bad\x1b[31m red‮ title\x00", c.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.ArchiveSession(bg, "default", root.ID, human)
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\x00‮") || strings.Contains(err.Error(), `\x`) || strings.Contains(err.Error(), `\u`) || !strings.Contains(err.Error(), "red") {
		t.Fatalf("refusal: %q", err)
	}
}
