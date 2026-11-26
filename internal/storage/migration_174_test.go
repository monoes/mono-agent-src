package storage

import (
	"path/filepath"
	"testing"
)

// Migration 174 records, on a kept dynamic-org worker, the claude session of its last reported cost, the total it had
// reached and its token counters. It is additive: a worker stored before it keeps its row with nothing to subtract, and a second run
// changes nothing.
func TestMigration174KeepsStoredWorkersWithNothingToSubtract(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrationsBelow(t, db, 174)
	for _, stmt := range []string{
		`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES ('c', 'default', 'monomind', 'n', 'n')`,
		`INSERT INTO ai_chat_org_workers (profile_id, conversation_id, agent_id, turn_id, runtime, session_id, updated_at) VALUES ('default', 'c', 'old', 't', 'claude', 's', 'n')`,
	} {
		if _, err := db.DB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("apply migrations, run %d: %v", run, err)
		}
		var session, resumable string
		var usd float64
		var models string
		if err := db.DB.QueryRow(`SELECT session_id, cost_session, cost_usd, cost_models FROM ai_chat_org_workers WHERE agent_id = 'old'`).Scan(&resumable, &session, &usd, &models); err != nil || resumable != "s" || session != "" || usd != 0 || models != "" {
			t.Fatalf("run %d: a worker stored before the migration: %q %q %v %q %v", run, resumable, session, usd, models, err)
		}
	}
}
