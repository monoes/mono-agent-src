package tasks

import "testing"

// Review finding 5: one corrupt row (a hand-made parent cycle) is skipped and reported, not a failure of
// the whole profile list.
func TestListSessionsSkipsACorruptRow(t *testing.T) {
	s, db, _ := newTestStore(t)
	a := mustSession(t, s, "default", "a")
	b := mustSession(t, s, "default", "b")
	good := mustSession(t, s, "default", "good")
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE task_sessions SET parent_session_id = ? WHERE id = ?`, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSessions(bg, "default", SessionFilter{}, human)
	if err != nil || len(list) != 1 || list[0].ID != good.ID {
		t.Fatalf("the list: %v %+v", err, list)
	}
	list, skipped, err := s.ListSessionsChecked(bg, "default", SessionFilter{}, human)
	if err != nil || len(list) != 1 || len(skipped) != 2 || skipped[0].Reason == "" {
		t.Fatalf("checked: %v %+v %+v", err, list, skipped)
	}
	tree, err := s.SessionTree(bg, "default", false, human)
	if err != nil || len(tree) != 1 {
		t.Fatalf("the tree: %v %+v", err, tree)
	}
}
