package ai

import (
	"errors"
	"slices"
	"sort"
	"testing"
)

// The answers a conversation's questions were given go with it.
func TestChatEvents_DeleteConversationRemovesItsAnswers(t *testing.T) {
	s, db := newChatEventStore(t)
	gone, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	kept, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, id := range []string{gone.ID, kept.ID} {
		if _, err := db.Exec(`INSERT INTO ai_chat_answers (profile_id, conversation_id, turn_id, agent_id, question_id, text, created_at) VALUES ('p1', ?, 't-' || ?, 'a', 'q', 'an answer', 'now')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteConversation(gone.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	count := func(id string) (n int) {
		if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_answers WHERE conversation_id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(gone.ID); n != 0 {
		t.Errorf("%d answer(s) of the deleted conversation survived", n)
	}
	if n := count(kept.ID); n != 1 {
		t.Errorf("the other conversation has %d answer(s)", n)
	}
}

// Every table that names a conversation has a decision for what deleting one does to its rows.
// A new table or column that names a conversation fails here until it is listed: decide whether
// DeleteConversation removes, closes or revokes its rows, and add the case to a test.
//
// The test finds columns by name (anything containing "conversation") plus the columns in
// conversationAliases, which hold a conversation id under another name. A column like that is not
// found by name: add it to conversationAliases when you add it to the schema.
func TestChatEvents_EveryConversationColumnHasADecision(t *testing.T) {
	_, db := newChatEventStore(t)
	// Columns that hold a conversation id under a name that does not say so.
	conversationAliases := map[string]bool{
		"coder_session_spawns.parent": true,
		"coder_session_spawns.child":  true,
	}
	decided := map[string]string{
		"coder_session_spawns.parent":                    "deleted with the parent's spawn log",
		"coder_session_spawns.child":                     "kept on purpose: removing it would hand the master its spawn-rate slot back",
		"agent_tokens.bound_conversation":                "revoked with the delete; none can be issued for a conversation that is gone",
		"ai_budget_reservations.conversation_id":         "open: held until the spend is synced",
		"ai_chat_answers.conversation_id":                "deleted",
		"ai_chat_events.conversation_id":                 "deleted",
		"ai_chat_org_workers.conversation_id":            "deleted with its conversation (foreign key)",
		"ai_chat_turns.conversation_id":                  "deleted",
		"ai_tasks.conversation_id":                       "token revoked and open task cancelled",
		"coder_mission_children.child_conversation_id":   "closed (state gone)",
		"coder_mission_children.lead_conversation_id":    "deleted",
		"coder_mission_children.pending_conversation_id": "closed (state gone)",
		"coder_mission_policy.lead_conversation_id":      "deleted",
		"coder_session_commands.conversation_id":         "deleted",
		"coder_session_decisions.conversation_id":        "deleted",
		"coder_session_host_turns.conversation_id":       "deleted",
		"coder_session_person_items.conversation_id":     "deleted",
		"coder_session_questions.conversation_id":        "deleted",
		"coder_session_tokens.conversation_id":           "deleted",
		"coder_session_turn_caps.conversation_id":        "deleted",
		"coder_sessions.conversation_id":                 "deleted",
		"coder_sessions.parent_conversation_id":          "children detached to solo",
		"task_sessions.conversation_id":                  "open: a mission session of the deleted lead keeps running",
	}
	rows, err := db.Query(`SELECT m.name || '.' || p.name FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' AND (p.name LIKE '%conversation%' OR m.name || '.' || p.name IN ('coder_session_spawns.parent', 'coder_session_spawns.child'))`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		found = append(found, col)
	}
	sort.Strings(found)
	for alias := range conversationAliases {
		if !slices.Contains(found, alias) {
			t.Errorf("%s is listed as an alias of a conversation id but no longer exists", alias)
		}
	}
	seen := map[string]bool{}
	for _, col := range found {
		seen[col] = true
		if _, ok := decided[col]; !ok {
			t.Errorf("%s names a conversation and has no decision for what deleting one does to it", col)
		}
	}
	for col := range decided {
		if !seen[col] {
			t.Errorf("%s is listed but no longer exists", col)
		}
	}
}

// An answer for a conversation that is gone is refused: the answer's insert is one statement that
// holds only while the conversation exists, so a delete that wins leaves no row behind.
func TestChatEvents_AddAnswerRefusesADeletedConversation(t *testing.T) {
	s, db := newChatEventStore(t)
	c, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	if err := s.AddAnswer("p1", c.ID, "t1", "a", "q", "yes"); err != nil {
		t.Fatalf("an answer to a live conversation: %v", err)
	}
	if err := s.DeleteConversation(c.ID, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAnswer("p1", c.ID, "t2", "a", "q", "late"); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("a late answer: %v, want ErrConversationNotFound", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_answers WHERE conversation_id = ?`, c.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d answer row(s) for the deleted conversation, %v", n, err)
	}
	other, _ := s.CreateConversation("p2", "agent", "general", "claude", "", "")
	if err := s.AddAnswer("p1", other.ID, "t3", "a", "q", "x"); !errors.Is(err, ErrConversationNotFound) {
		t.Errorf("an answer under another profile's conversation: %v", err)
	}
}
