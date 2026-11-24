package tasks

import "testing"

// R16-D6: a stale claim is re-offered under the same rules as a Ready task:
// not blocked, not an epic, every dependency settled.
func TestStaleClaimFollowsTheReadyRules(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, ts := runningWith(t, s, "a", "dep")
	got := claimNext(t, s, se.ID, "w1")
	if got == nil {
		t.Fatal("nothing claimed")
	}
	held := got.ID
	other := claimNext(t, s, se.ID, "w3") // nothing else stays Ready
	if other == nil {
		t.Fatal("second task not claimed")
	}
	dep := other.ID
	_ = ts
	stale := func(extra string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE tasks SET claim_until = '2000-01-01T00:00:00Z' WHERE id = ?`, held); err != nil {
			t.Fatal(err)
		}
		if extra != "" {
			if _, err := db.Exec(extra, held); err != nil {
				t.Fatal(err)
			}
		}
	}

	stale(`UPDATE tasks SET blocked = 1 WHERE id = ?`)
	if tk, _ := s.SessionNext(bg, "default", se.ID, bot("w2"), false, 0); tk != nil && tk.ID == held {
		t.Errorf("a blocked task with a stale claim was re-offered")
	}
	stale(`UPDATE tasks SET blocked = 0, kind = 'epic' WHERE id = ?`)
	if tk, _ := s.SessionNext(bg, "default", se.ID, bot("w2"), false, 0); tk != nil && tk.ID == held {
		t.Errorf("an epic with a stale claim was re-offered")
	}
	stale(`UPDATE tasks SET kind = 'task' WHERE id = ?`)
	if _, err := db.Exec(`INSERT INTO task_deps (task_id, depends_on_task_id) VALUES (?, ?)`, held, dep); err != nil {
		t.Fatal(err)
	}
	if tk, _ := s.SessionNext(bg, "default", se.ID, bot("w2"), false, 0); tk != nil && tk.ID == held {
		t.Errorf("a task with an unfinished dependency and a stale claim was re-offered")
	}
}
