package tasks

import "testing"

// Review 47: a bound agent's tree is read from its subtree in SQL, so a row elsewhere in the database
// (another profile's, or a corrupt one outside the subtree) is neither shown nor able to fail the read,
// and a hand-made cycle of parents ends at the depth cap.
func TestSessionTreeOfABoundAgentReadsOnlyItsSubtree(t *testing.T) {
	s, db, _ := newTestStore(t)
	addProfile(t, db, "other")
	root := mustSession(t, s, "default", "root")
	kid, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: root.ID}, human)
	if err != nil {
		t.Fatal(err)
	}
	apart := mustSession(t, s, "default", "apart")
	foreign := mustSession(t, s, "other", "foreign")
	// another profile's session that claims the bound session as its parent must not appear below it
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, root.ID, foreign.ID); err != nil {
		t.Fatal(err)
	}
	// a corrupt row outside the subtree (an unreadable timestamp) must not fail the bound agent's read
	if _, err := db.Exec(`UPDATE task_sessions SET updated_at = 'not a time' WHERE id = ?`, apart.ID); err != nil {
		t.Fatal(err)
	}
	lead := Actor{Kind: Agent, Name: "lead", Session: root.ID}
	tree, err := s.SessionTree(bg, "default", false, lead)
	if err != nil || len(tree) != 1 || tree[0].ID != root.ID || len(tree[0].Children) != 1 || tree[0].Children[0].ID != kid.ID {
		t.Fatalf("the bound tree: %v %+v", err, tree)
	}
	if len(tree[0].Children[0].Children) != 0 {
		t.Fatalf("something leaked below the kid: %+v", tree)
	}
}

func TestSessionTreeOfABoundAgentEndsOnACycle(t *testing.T) {
	s, db, _ := newTestStore(t)
	a := mustSession(t, s, "default", "a")
	b := mustSession(t, s, "default", "b")
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	tree, err := s.SessionTree(bg, "default", false, Actor{Kind: Agent, Name: "lead", Session: a.ID})
	if err != nil || len(tree) != 0 {
		t.Fatalf("a cycle is skipped as corrupt, not looped on: %v %+v", err, tree)
	}
}
