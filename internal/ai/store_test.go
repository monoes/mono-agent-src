package ai

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/ai-test.db")
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db.DB
}

func TestStoreInitTables(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}
	_ = store

	// The provider table is gone: a migration drops it and the store must
	// not bring it back.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ai_providers'`).Scan(&n); err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	if n != 0 {
		t.Error("NewAIStore created ai_providers")
	}

	// Verify ai_chat_messages (migration 095) exists by inserting a raw row.
	_, err = db.Exec(`INSERT INTO ai_chat_messages (id, workflow_id, role, content, created_at)
		VALUES ('msg1', 'wf1', 'user', 'hello', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert into ai_chat_messages: %v", err)
	}
}

func TestChatMessageCRUD(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	wfID := "workflow-1"

	// Save messages
	m1 := ChatMessage{
		ID:         "m1",
		WorkflowID: wfID,
		Role:       "user",
		Content:    "Hello",
		CreatedAt:  "2025-01-01T00:00:01Z",
	}
	m2 := ChatMessage{
		ID:         "m2",
		WorkflowID: wfID,
		Role:       "assistant",
		Content:    "Hi there!",
		ProviderID: "p1",
		Model:      "gpt-4o",
		TokenCount: 42,
		CreatedAt:  "2025-01-01T00:00:02Z",
	}

	if err := store.SaveChatMessage(m1); err != nil {
		t.Fatalf("SaveChatMessage m1: %v", err)
	}
	if err := store.SaveChatMessage(m2); err != nil {
		t.Fatalf("SaveChatMessage m2: %v", err)
	}

	// Get history
	history, err := store.GetChatHistory(wfID, "")
	if err != nil {
		t.Fatalf("GetChatHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("GetChatHistory len = %d, want 2", len(history))
	}
	if history[0].ID != "m1" || history[1].ID != "m2" {
		t.Errorf("history order: got [%s, %s], want [m1, m2]", history[0].ID, history[1].ID)
	}
	if history[1].TokenCount != 42 {
		t.Errorf("TokenCount = %d, want 42", history[1].TokenCount)
	}
	if history[0].ProfileID != "default" {
		t.Errorf("ProfileID = %q, want %q (empty normalizes to default)", history[0].ProfileID, "default")
	}

	// Clear history
	if err := store.ClearChatHistory(wfID, ""); err != nil {
		t.Fatalf("ClearChatHistory: %v", err)
	}
	history, err = store.GetChatHistory(wfID, "")
	if err != nil {
		t.Fatalf("GetChatHistory after clear: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("GetChatHistory after clear: len = %d, want 0", len(history))
	}
}

// TestChatMessageProfileIsolation is a regression test for a real
// cross-profile data leak: ai_chat_messages had no profile scoping at all,
// and the AI chat panel always uses the literal, non-unique workflow_id
// "general" for its global per-profile assistant chat. Without this check,
// switching the active profile in the GUI would show — and let you clear —
// another profile's "general" conversation.
func TestChatMessageProfileIsolation(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	const wf = "general"
	if err := store.SaveChatMessage(ChatMessage{
		ID: "a1", WorkflowID: wf, ProfileID: "profile-a", Role: "user", Content: "profile a secret",
		SessionID: "sess-a", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveChatMessage (profile-a): %v", err)
	}
	if err := store.SaveChatMessage(ChatMessage{
		ID: "b1", WorkflowID: wf, ProfileID: "profile-b", Role: "user", Content: "profile b secret",
		SessionID: "sess-b", CreatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("SaveChatMessage (profile-b): %v", err)
	}

	// GetChatHistory: profile B must not see profile A's messages.
	historyB, err := store.GetChatHistory(wf, "profile-b")
	if err != nil {
		t.Fatalf("GetChatHistory(profile-b): %v", err)
	}
	if len(historyB) != 1 || historyB[0].ID != "b1" {
		t.Errorf("GetChatHistory(profile-b) = %+v, want only b1", historyB)
	}

	// ListChatSessions: profile B must not see profile A's session.
	sessionsB, err := store.ListChatSessions(wf, "profile-b")
	if err != nil {
		t.Fatalf("ListChatSessions(profile-b): %v", err)
	}
	if len(sessionsB) != 1 || sessionsB[0].SessionID != "sess-b" {
		t.Errorf("ListChatSessions(profile-b) = %+v, want only sess-b", sessionsB)
	}

	// GetSessionMessages: profile B must not read profile A's session by ID,
	// even if it somehow learned the session ID.
	msgs, err := store.GetSessionMessages(wf, "sess-a", "profile-b")
	if err != nil {
		t.Fatalf("GetSessionMessages(sess-a, profile-b): %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("GetSessionMessages(sess-a, profile-b) = %+v, want empty (cross-profile read)", msgs)
	}

	// ClearChatHistory: clearing profile B's history must not delete profile A's.
	if err := store.ClearChatHistory(wf, "profile-b"); err != nil {
		t.Fatalf("ClearChatHistory(profile-b): %v", err)
	}
	historyA, err := store.GetChatHistory(wf, "profile-a")
	if err != nil {
		t.Fatalf("GetChatHistory(profile-a) after profile-b clear: %v", err)
	}
	if len(historyA) != 1 || historyA[0].ID != "a1" {
		t.Errorf("profile-a history after profile-b ClearChatHistory = %+v, want a1 untouched", historyA)
	}
}

// TestChatMessageLegacyAmbiguousRowsAreExcluded is the plan-mandated
// "legacy ambiguous exclusion" case: a row saved before profile_id existed
// (simulated here by inserting raw SQL the way the ALTER TABLE backfill
// would have left it, bypassing SaveChatMessage entirely) must not be
// silently attributed to whichever profile happens to be named "default" —
// it must be invisible to every profile, including "default" itself. This
// is a deliberate departure from the COALESCE(profile_id,'default')
// convention app-config tables use: chat history rows are per-user content,
// not app config, so misattributing one profile's old conversation to another is a real
// privacy concern, not just a minor inconvenience.
func TestChatMessageLegacyAmbiguousRowsAreExcluded(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	const wf = "general"
	// Simulate a pre-migration row: profile_id left at the column's '' default,
	// never touched by SaveChatMessage's normalization.
	if _, err := db.Exec(
		`INSERT INTO ai_chat_messages (id, workflow_id, role, content, session_id, profile_id, created_at)
		 VALUES ('legacy1', ?, 'user', 'ambiguous old message', 'sess-legacy', '', '2025-01-01T00:00:00Z')`,
		wf,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	for _, profileID := range []string{"default", "profile-a", ""} {
		history, err := store.GetChatHistory(wf, profileID)
		if err != nil {
			t.Fatalf("GetChatHistory(%q): %v", profileID, err)
		}
		if len(history) != 0 {
			t.Errorf("GetChatHistory(%q) = %+v, want empty (ambiguous legacy row must never surface)", profileID, history)
		}

		sessions, err := store.ListChatSessions(wf, profileID)
		if err != nil {
			t.Fatalf("ListChatSessions(%q): %v", profileID, err)
		}
		if len(sessions) != 0 {
			t.Errorf("ListChatSessions(%q) = %+v, want empty", profileID, sessions)
		}

		msgs, err := store.GetSessionMessages(wf, "sess-legacy", profileID)
		if err != nil {
			t.Fatalf("GetSessionMessages(%q): %v", profileID, err)
		}
		if len(msgs) != 0 {
			t.Errorf("GetSessionMessages(%q) = %+v, want empty", profileID, msgs)
		}

		// Nor can any profile clear it away by accident.
		if err := store.ClearChatHistory(wf, profileID); err != nil {
			t.Fatalf("ClearChatHistory(%q): %v", profileID, err)
		}
	}

	var stillThere int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_messages WHERE id = 'legacy1'`).Scan(&stillThere); err != nil {
		t.Fatalf("count legacy row: %v", err)
	}
	if stillThere != 1 {
		t.Errorf("legacy ambiguous row was deleted by a scoped ClearChatHistory call; it must survive on disk untouched")
	}
}

// TestChatHistorySameTimestampKeepsInsertOrder is the RB3 tie-ordering
// regression test: messages saved within the same second must come back in
// insert order after reload (rowid tiebreak, not an unstable sort).
func TestChatHistorySameTimestampKeepsInsertOrder(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	const wfID = "wf-order"
	const sameSecond = "2025-06-15T12:00:00Z"
	for i := 0; i < 5; i++ {
		m := ChatMessage{
			ID:         fmt.Sprintf("m%d", i),
			WorkflowID: wfID,
			Role:       "user",
			Content:    fmt.Sprintf("message %d", i),
			CreatedAt:  sameSecond,
		}
		if err := store.SaveChatMessage(m); err != nil {
			t.Fatalf("SaveChatMessage m%d: %v", i, err)
		}
	}

	history, err := store.GetChatHistory(wfID, "")
	if err != nil {
		t.Fatalf("GetChatHistory: %v", err)
	}
	if len(history) != 5 {
		t.Fatalf("GetChatHistory len = %d, want 5", len(history))
	}
	for i, m := range history {
		if m.ID != fmt.Sprintf("m%d", i) {
			t.Errorf("history[%d].ID = %q, want m%d (insert order not preserved)", i, m.ID, i)
		}
	}
}

// TestChatSessions covers the resumable-session grouping introduced for
// AIChatPanel's session continuity + past-sessions list: messages sharing a
// SessionID within one WorkflowID must group into one ChatSession, ordered
// most-recently-updated first, and GetSessionMessages must return only that
// session's rows.
func TestChatSessions(t *testing.T) {
	db := openTestDB(t)
	store, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}

	const wf = "general"
	save := func(id, role, content, sessionID, createdAt string) {
		t.Helper()
		if err := store.SaveChatMessage(ChatMessage{
			ID: id, WorkflowID: wf, Role: role, Content: content,
			ProviderID: "codex", Model: "gpt-5", SessionID: sessionID, CreatedAt: createdAt,
		}); err != nil {
			t.Fatalf("SaveChatMessage(%s): %v", id, err)
		}
	}

	// Session "s1": two turns, earlier.
	save("m1", "user", "hi, remember 42", "s1", "2026-01-01T00:00:00Z")
	save("m2", "assistant", "ok, 42 noted", "s1", "2026-01-01T00:00:01Z")
	save("m3", "user", "what number?", "s1", "2026-01-01T00:00:02Z")
	save("m4", "assistant", "42", "s1", "2026-01-01T00:00:03Z")
	// Session "s2": one turn, later — should sort first.
	save("m5", "user", "new topic entirely, this is a much longer message that should get truncated in the preview text shown in the past-sessions dropdown list", "s2", "2026-01-02T00:00:00Z")
	save("m6", "assistant", "sure", "s2", "2026-01-02T00:00:01Z")
	// A message with no session_id (legacy row) must be excluded entirely.
	save("m7", "user", "legacy row", "", "2026-01-03T00:00:00Z")

	sessions, err := store.ListChatSessions(wf, "")
	if err != nil {
		t.Fatalf("ListChatSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("ListChatSessions returned %d sessions, want 2 (legacy no-session-id row must be excluded)", len(sessions))
	}
	if sessions[0].SessionID != "s2" {
		t.Errorf("sessions[0].SessionID = %q, want %q (most-recently-updated first)", sessions[0].SessionID, "s2")
	}
	if sessions[1].SessionID != "s1" {
		t.Errorf("sessions[1].SessionID = %q, want %q", sessions[1].SessionID, "s1")
	}
	if sessions[1].MessageCount != 4 {
		t.Errorf("sessions[1] (s1) MessageCount = %d, want 4", sessions[1].MessageCount)
	}
	if sessions[1].Runtime != "codex" || sessions[1].Model != "gpt-5" {
		t.Errorf("sessions[1] (s1) Runtime/Model = %q/%q, want codex/gpt-5", sessions[1].Runtime, sessions[1].Model)
	}
	if got := []rune(sessions[0].Preview); len(got) > 81 { // 80 chars + ellipsis
		t.Errorf("sessions[0].Preview not truncated: %d runes", len(got))
	}

	msgs, err := store.GetSessionMessages(wf, "s1", "")
	if err != nil {
		t.Fatalf("GetSessionMessages: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("GetSessionMessages(s1) returned %d messages, want 4", len(msgs))
	}
	for _, m := range msgs {
		if m.SessionID != "s1" {
			t.Errorf("GetSessionMessages(s1) returned a message from session %q", m.SessionID)
		}
	}
	if msgs[3].Content != "42" {
		t.Errorf("GetSessionMessages(s1)[3].Content = %q, want %q (ascending order)", msgs[3].Content, "42")
	}
}
