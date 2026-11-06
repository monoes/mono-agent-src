package coderhost

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
	"github.com/monoes/mono-agent/internal/testdb"
)

// Migration 163: coder_sessions.chat_mode, backfilled from the old Mission flag, and locked once the conversation has
// its first message (a queued send or a turn).
func chatModeOf(t *testing.T, st *Store, conv string) string {
	t.Helper()
	var m string
	if err := st.db.QueryRow(`SELECT chat_mode FROM coder_sessions WHERE profile_id = 'p' AND conversation_id = ?`, conv).Scan(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMigration163BackfillsMissionAndLocksAtTheFirstMessage(t *testing.T) {
	db := testdb.Open(t)
	t.Cleanup(func() { db.Close() })
	mig, err := data.MigrationsFS.ReadFile("migrations/163_coder_sessions_chat_mode.sql")
	if err != nil {
		t.Fatal(err)
	}
	// an install at 162: the column and the trigger are not there yet
	for _, q := range []string{"DR" + "OP TRIGGER coder_sessions_chat_mode_locked", "ALTER TABLE coder_sessions DR" + "OP COLUMN chat_mode"} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for conv, settings := range map[string]string{"on": `{"mission":true}`, "off": `{"mission":false}`, "none": `{}`, "junk": `not json`} {
		if _, err := db.DB.Exec(`INSERT INTO coder_sessions (profile_id, conversation_id, settings, created_at, updated_at) VALUES ('p', ?, ?, 'x', 'x')`, conv, settings); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.DB.Exec(string(mig)); err != nil {
		t.Fatal(err)
	}
	st := NewStore(db.DB)
	for conv, want := range map[string]string{"on": "mission", "off": "off", "none": "off", "junk": "off"} {
		if got := chatModeOf(t, st, conv); got != want {
			t.Errorf("backfill of %s = %q, want %q", conv, got, want)
		}
	}
	// the column only holds the three modes
	if _, err := db.DB.Exec(`UPDATE coder_sessions SET chat_mode = 'doomsday' WHERE conversation_id = 'off'`); err == nil {
		t.Error("an unknown mode was stored")
	}
	// before the first message the mode changes freely
	if _, err := db.DB.Exec(`UPDATE coder_sessions SET chat_mode = 'open' WHERE conversation_id = 'off'`); err != nil {
		t.Fatalf("a change before any message was refused: %v", err)
	}
	// a queued send locks it, a turn locks it, and a write of the same value is no change
	if _, err := db.DB.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, created_at, updated_at) VALUES ('p', 'on', 'send', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES ('none', 'p', 'agent', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, created_at, updated_at) VALUES ('t1', 'none', 'p', 'hi', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	for conv, to := range map[string]string{"on": "off", "none": "mission"} {
		_, err := db.DB.Exec(`UPDATE coder_sessions SET chat_mode = ? WHERE conversation_id = ?`, to, conv)
		if err == nil || !strings.Contains(err.Error(), "chat_mode is locked") {
			t.Errorf("%s: a change after the first message was not refused: %v", conv, err)
		}
	}
	if _, err := db.DB.Exec(`UPDATE coder_sessions SET chat_mode = 'mission', settings = '{}' WHERE conversation_id = 'on'`); err != nil {
		t.Errorf("rewriting the same mode after the first message failed: %v", err)
	}
	// another conversation's messages do not lock this one
	if _, err := db.DB.Exec(`UPDATE coder_sessions SET chat_mode = 'mission' WHERE conversation_id = 'junk'`); err != nil {
		t.Errorf("a change in a conversation without messages was refused: %v", err)
	}
}
