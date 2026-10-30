package tasks

import (
	"errors"
	"strings"
	"testing"
)

func child(t *testing.T, s *Store, profile string, parent int64, title string) Session {
	t.Helper()
	se, err := s.CreateSession(bg, profile, SessionInput{Title: title, ParentSessionID: parent, Kind: SessionKindResearch}, human)
	if err != nil {
		t.Fatalf("child %q: %v", title, err)
	}
	return se
}

func TestSessionTreeDefaultsAreBoardWithNoParent(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "root")
	if se.Kind != SessionKindBoard || se.ParentSessionID != 0 || se.Depth != 1 || se.ApprovalBasis != "" {
		t.Fatalf("defaults: %+v", se)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", Kind: "bogus"}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown kind: %v", err)
	}
	se = mustRun(t, s, "default", func() Session { mustTask(t, s, "default", se.ID, "t"); return se }())
	if se.ApprovalBasis != "operator" {
		t.Fatalf("approval basis: %q", se.ApprovalBasis)
	}
}

func TestSessionParentMustExistInTheSameProfile(t *testing.T) {
	s, db, _ := newTestStore(t)
	addProfile(t, db, "other")
	foreign := mustSession(t, s, "other", "theirs")
	for _, parent := range []int64{999, foreign.ID} {
		_, err := s.CreateSession(bg, "default", SessionInput{Title: "c", ParentSessionID: parent}, human)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("parent %d: %v", parent, err)
		}
	}
	if n := sessionRows(t, s, "default"); n != 0 {
		t.Fatalf("a refused child left %d rows", n)
	}
}

func TestSessionDepthIsComputedAndCapped(t *testing.T) {
	s, _, _ := newTestStore(t)
	cur := mustSession(t, s, "default", "l1")
	for want := 2; want <= HardMaxSessionDepth; want++ {
		cur = child(t, s, "default", cur.ID, "level")
		if cur.Depth != want || cur.Kind != SessionKindResearch {
			t.Fatalf("level %d: %+v", want, cur)
		}
	}
	before := sessionRows(t, s, "default")
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "too deep", ParentSessionID: cur.ID}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("level 5: %v", err)
	}
	if sessionRows(t, s, "default") != before {
		t.Fatal("a refused level left a row")
	}
	got, err := s.GetSession(bg, "default", cur.ID, human)
	if err != nil || got.Depth != HardMaxSessionDepth {
		t.Fatalf("stored depth: %+v %v", got, err)
	}
}

func TestSessionChainRefusesACycle(t *testing.T) {
	s, db, _ := newTestStore(t)
	a := mustSession(t, s, "default", "a")
	b := child(t, s, "default", a.ID, "b")
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateSession(bg, "default", SessionInput{Title: "c", ParentSessionID: a.ID}, human)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a cycle must be named: %v", err)
	}
}

func TestSessionChildNeverExceedsParent(t *testing.T) {
	s, _, _ := newTestStore(t)
	par, err := s.CreateSession(bg, "default", SessionInput{Title: "p", AccessLevel: "coding", BudgetUSD: 10, MaxTasks: 30, MaxDepth: 2}, human)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []SessionInput{
		{Title: "c", AccessLevel: "full"},
		{Title: "c", BudgetUSD: 11},
		{Title: "c", MaxTasks: 31},
		{Title: "c", MaxDepth: 3},
	} {
		in.ParentSessionID = par.ID
		if _, err := s.CreateSession(bg, "default", in, human); !errors.Is(err, ErrLimit) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if n := sessionRows(t, s, "default"); n != 1 {
		t.Fatalf("refusals left %d rows", n)
	}
	// Unset caps are taken from the parent (the lower of default and parent), an unset budget stays unset; explicit ones within the parent's pass.
	c, err := s.CreateSession(bg, "default", SessionInput{Title: "c", ParentSessionID: par.ID}, human)
	if err != nil || c.BudgetUSD != 0 || c.MaxTasks != 30 || c.MaxDepth != 2 || c.AccessLevel != "scoped" {
		t.Fatalf("inherited: %+v %v", c, err)
	}
	c, err = s.CreateSession(bg, "default", SessionInput{Title: "c2", ParentSessionID: par.ID, AccessLevel: "coding", BudgetUSD: 4, MaxTasks: 5}, human)
	if err != nil || c.BudgetUSD != 4 || c.MaxTasks != 5 || c.AccessLevel != "coding" {
		t.Fatalf("within: %+v %v", c, err)
	}
}

func TestSessionAgentStartsOnlyBelowTheSessionItWorksIn(t *testing.T) {
	s, _, _ := newTestStore(t)
	root := mustSession(t, s, "default", "root")
	other := mustSession(t, s, "default", "other")
	lead := Actor{Kind: Agent, Name: "lead", Session: root.ID}
	se, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: root.ID}, lead)
	if err != nil || se.ParentSessionID != root.ID || se.CreatedBy != "lead" {
		t.Fatalf("below own: %+v %v", se, err)
	}
	grand, err := s.CreateSession(bg, "default", SessionInput{Title: "grandkid", ParentSessionID: se.ID}, lead)
	if err != nil || grand.Depth != 3 {
		t.Fatalf("below a descendant: %+v %v", grand, err)
	}
	before := sessionRows(t, s, "default")
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: other.ID}, lead); !errors.Is(err, ErrNotFound) {
		t.Fatalf("below a session it does not work in: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x"}, lead); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a bound agent at top level: %v", err)
	}
	free := bot("free")
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: root.ID}, free); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an unbound agent below a session: %v", err)
	}
	worker := Actor{Kind: Agent, Name: "w", Session: root.ID, Task: 1}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "x", ParentSessionID: root.ID}, worker); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a task worker: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "top"}, free); err != nil {
		t.Fatalf("an unbound agent at top level, as today: %v", err)
	}
	if sessionRows(t, s, "default") != before+1 {
		t.Fatal("refusals left rows")
	}
}

func TestRenameSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	chatRow(t, s, "c1")
	chatRow(t, s, "c2")
	root := mustSession(t, s, "default", "root")
	// an agent renames only a draft whose creation recorded its (verified) chat
	lead := Actor{Kind: Agent, Name: "lead", Session: root.ID, Conversation: "c1"}
	mine, _ := s.CreateSession(bg, "default", SessionInput{Title: "mine", ParentSessionID: root.ID}, lead)
	freeBot := Actor{Kind: Agent, Name: "free", Conversation: "c2"}
	free, _ := s.CreateSession(bg, "default", SessionInput{Title: "free"}, freeBot)

	got, err := s.RenameSession(bg, "default", root.ID, "  New\nname ", human)
	if err != nil || got.Title != "New name" || got.Goal != root.Goal || got.State != root.State || got.Rev <= root.Rev {
		t.Fatalf("operator rename: %+v %v", got, err)
	}
	if got, err = s.RenameSession(bg, "default", mine.ID, "renamed", lead); err != nil || got.Title != "renamed" {
		t.Fatalf("an agent renames its own: %+v %v", got, err)
	}
	// not its own, whether below its session or elsewhere
	if _, err = s.RenameSession(bg, "default", root.ID, "hijack", lead); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("a session the agent did not create: %v", err)
	}
	if _, err = s.RenameSession(bg, "default", free.ID, "hijack", bot("other")); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("another agent's session: %v", err)
	}
	if _, err = s.RenameSession(bg, "default", free.ID, "again", freeBot); err != nil {
		t.Fatalf("the creator renames: %v", err)
	}
	if _, err = s.RenameSession(bg, "default", free.ID, "x", lead); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a bound agent outside its subtree: %v", err)
	}
	if _, err = s.RenameSession(bg, "default", mine.ID, "  ", human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty title: %v", err)
	}
	if _, err = s.RenameSession(bg, "default", mine.ID, "x", Actor{Kind: Capture, Name: "os"}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("capture: %v", err)
	}
	if _, err = s.RenameSession(bg, "default", 999, "x", human); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	evs, err := s.SessionEvents(bg, "default", mine.ID, 0, human)
	if err != nil || evs[len(evs)-1].Kind != "renamed" || !strings.Contains(evs[len(evs)-1].Note, "mine -> renamed") {
		t.Fatalf("event: %+v %v", evs, err)
	}
	if g, _ := s.GetSession(bg, "default", mine.ID, human); g.Title != "renamed" {
		t.Fatalf("a refused rename changed the title: %+v", g)
	}
}

func TestSessionTreeNestsByIDAndBindsAgents(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSession(t, s, "default", "a")
	b := mustSession(t, s, "default", "b")
	a2 := child(t, s, "default", a.ID, "a2")
	a1 := child(t, s, "default", a.ID, "a1")
	a11 := child(t, s, "default", a2.ID, "a11")
	tree, err := s.SessionTree(bg, "default", false, human)
	if err != nil || len(tree) != 2 || tree[0].ID != a.ID || tree[1].ID != b.ID {
		t.Fatalf("roots: %+v %v", tree, err)
	}
	if k := tree[0].Children; len(k) != 2 || k[0].ID != a2.ID || k[1].ID != a1.ID || len(k[0].Children) != 1 || k[0].Children[0].ID != a11.ID {
		t.Fatalf("children ordered by id: %+v", k)
	}
	if len(tree[1].Children) != 0 || tree[1].Children == nil {
		t.Fatalf("a leaf has an empty children list: %+v", tree[1])
	}
	sub, err := s.SessionTree(bg, "default", false, Actor{Kind: Agent, Name: "lead", Session: a2.ID})
	if err != nil || len(sub) != 1 || sub[0].ID != a2.ID || len(sub[0].Children) != 1 {
		t.Fatalf("a bound agent sees its subtree: %+v %v", sub, err)
	}
	if _, err := s.SessionTree(bg, "default", false, Actor{Kind: Capture}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("capture: %v", err)
	}
}

func TestAnAgentBelowAParentMayNarrowTheCapsButNeverRaiseThem(t *testing.T) {
	s, _, _ := newTestStore(t)
	par, err := s.CreateSession(bg, "default", SessionInput{Title: "p", BudgetUSD: 10, MaxTasks: 50}, human)
	if err != nil {
		t.Fatal(err)
	}
	lead := Actor{Kind: Agent, Name: "lead-a", Session: par.ID}
	ok, err := s.CreateSession(bg, "default", SessionInput{Title: "c", ParentSessionID: par.ID, MaxTasks: 25}, lead)
	if err != nil || ok.MaxTasks != 25 || ok.BudgetUSD != 0 {
		t.Fatalf("narrowed = %+v, %v", ok, err)
	}
	for name, in := range map[string]SessionInput{
		"more tasks": {Title: "c", ParentSessionID: par.ID, MaxTasks: 51},
		"a budget":   {Title: "c", ParentSessionID: par.ID, BudgetUSD: 1},
		"depth":      {Title: "c", ParentSessionID: par.ID, MaxDepth: 1},
		"access":     {Title: "c", ParentSessionID: par.ID, AccessLevel: "read"},
	} {
		if _, err := s.CreateSession(bg, "default", in, lead); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "top", MaxTasks: 5}, Actor{Kind: Agent, Name: "loose"}); err == nil {
		t.Error("a top-level agent set a cap")
	}
}
