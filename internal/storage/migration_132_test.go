package storage

import (
	"path/filepath"
	"testing"
)

// Migration 132 moves the old wants-mission marker out of the error column of a
// pending command into wants_mission; other rows keep their error text.
func TestMigration132BackfillsTheMissionMarker(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	applyMigrationsBelow(t, db, 132)
	ins := func(turn, status, errText string) {
		t.Helper()
		if _, err := db.DB.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, status, error, created_at, updated_at)
			VALUES ('p', 'c', 'send', ?, 'm', ?, ?, 'now', 'now')`, turn, status, errText); err != nil {
			t.Fatal(err)
		}
	}
	ins("old", "pending", "wants-mission")
	ins("plain", "pending", "")
	ins("failed", "failed", "boom")

	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("apply migrations, run %d: %v", run, err)
		}
		for turn, want := range map[string][2]string{"old": {"1", ""}, "plain": {"0", ""}, "failed": {"0", "boom"}} {
			var wm, e string
			if err := db.DB.QueryRow(`SELECT CAST(wants_mission AS TEXT), error FROM coder_session_commands WHERE turn_id = ?`, turn).Scan(&wm, &e); err != nil {
				t.Fatal(err)
			}
			if wm != want[0] || e != want[1] {
				t.Errorf("run %d, %s: wants_mission=%s error=%q, want %v", run, turn, wm, e, want)
			}
		}
	}
	if _, err := db.DB.Exec(`UPDATE coder_session_commands SET wants_mission = 2`); err == nil {
		t.Error("wants_mission accepted 2")
	}
}
