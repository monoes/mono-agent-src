package ai

import "testing"

// Deleting a chat removes the asks it made, voids the normal asks addressed to it, and leaves the critical asks
// addressed to it: those wait for the person, not for that chat.
func TestChatEvents_DeleteConversationKeepsOthersCriticalAsks(t *testing.T) {
	s, db := newChatEventStore(t)
	asker, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	anc, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	other, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	ask := func(askerConv, ancConv, imp string) int64 {
		res, err := db.Exec(`INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at)
			VALUES ('p1', 1, ?, 2, ?, 'grandparent', ?, 'q', 1, 'open', 'now')`, askerConv, ancConv, imp)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if imp == "critical" {
			if _, err := db.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p1', ?, 'ancestor_question', ?, 'now')`, askerConv, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	critical := ask(asker.ID, anc.ID, "critical")
	normal := ask(asker.ID, anc.ID, "normal")
	own := ask(anc.ID, other.ID, "normal")
	if err := s.DeleteConversation(anc.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	status := func(id int64) (st string, n int) {
		_ = db.QueryRow(`SELECT COUNT(*) FROM coder_session_asks WHERE id = ?`, id).Scan(&n)
		_ = db.QueryRow(`SELECT status FROM coder_session_asks WHERE id = ?`, id).Scan(&st)
		return st, n
	}
	if st, n := status(critical); n != 1 || st != "open" {
		t.Errorf("another session's critical ask was destroyed or closed: n=%d status=%q", n, st)
	}
	var items int
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_session_person_items WHERE kind = 'ancestor_question' AND ref_id = ?`, critical).Scan(&items)
	if items != 1 {
		t.Errorf("the person item of that ask went with the ancestor: %d", items)
	}
	if st, n := status(normal); n != 1 || st != "void" {
		t.Errorf("a normal ask to the deleted chat: n=%d status=%q", n, st)
	}
	if _, n := status(own); n != 0 {
		t.Errorf("the deleted chat's own ask survived")
	}
}

func TestChatEvents_DeleteConversationOfTheAskerRemovesItsAsksAndItems(t *testing.T) {
	s, db := newChatEventStore(t)
	asker, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	if _, err := db.Exec(`INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, created_at)
		VALUES ('p1', 1, ?, 2, 'gone', 'grandparent', 'critical', 'q', 1, 'open', 'now')`, asker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p1', ?, 'ancestor_question', 1, 'now')`, asker.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversation(asker.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	var a, i int
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_session_asks`).Scan(&a)
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_session_person_items WHERE kind = 'ancestor_question'`).Scan(&i)
	if a != 0 || i != 0 {
		t.Errorf("asks=%d items=%d survived the asker's chat", a, i)
	}
}

// The spawn advisory's remembered refusal belongs to the lead that was refused: it goes with that lead's chat and
// leaves another lead's alone.
func TestChatEvents_DeleteConversationRemovesItsSpawnAdvice(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, c := range []string{gone.ID, kept.ID} {
		if _, err := db.Exec(`INSERT INTO coder_spawn_advice (profile_id, spawner, scope_hash, created_at) VALUES ('p1', ?, 'h', 'now')`, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_spawn_advice WHERE spawner = ?`, gone.ID).Scan(&n)
	if n != 0 {
		t.Errorf("%d advice row(s) of the deleted lead survived", n)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_spawn_advice WHERE spawner = ?`, kept.ID).Scan(&n)
	if n != 1 {
		t.Errorf("the other lead's advice: %d", n)
	}
}
