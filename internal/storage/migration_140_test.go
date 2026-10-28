package storage

import (
	"path/filepath"
	"testing"
)

// Migration 140 adds the conversation index the host's stop lookup uses. It is its own migration because 139 is
// already released.
func TestMigration140AddsTheCommandsConversationIndex(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrationsBelow(t, db, 140)
	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("apply migrations, run %d: %v", run, err)
		}
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_coder_session_commands_chat'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("index missing: n=%d err=%v", n, err)
	}
}
