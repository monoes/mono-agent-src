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

// The question a deleted chat asked sits in the recipient's queue as a command; it goes with the asker (it holds
// the full text and has no ask left to answer).
func TestChatEvents_DeleteConversationOfTheAskerRemovesItsQueuedQuestion(t *testing.T) {
	s, db := newChatEventStore(t)
	asker, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	anc, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	cmd := func(conv, msg string) int64 {
		res, err := db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at)
			VALUES ('p1', ?, 'send', 'ask-q-1', ?, '[]', 'pending', 'now', 'now')`, conv, msg)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	q, other := cmd(anc.ID, "private question"), cmd(anc.ID, "someone else's")
	if _, err := db.Exec(`INSERT INTO coder_session_asks (profile_id, asker_session, asker_conv, ancestor_session, ancestor_conv, level, importance, question, bytes, status, command_id, created_at)
		VALUES ('p1', 1, ?, 2, ?, 'grandparent', 'normal', 'private question', 1, 'open', ?, 'now')`, asker.ID, anc.ID, q); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversation(asker.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	var gotQ, gotOther int
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE id = ?`, q).Scan(&gotQ)
	_ = db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE id = ?`, other).Scan(&gotOther)
	if gotQ != 0 || gotOther != 1 {
		t.Errorf("queued question kept=%d (want 0), another command kept=%d (want 1)", gotQ, gotOther)
	}
}
