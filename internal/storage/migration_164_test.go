package storage

import "testing"

var missionGoalTables = []string{"mission_goals", "mission_goal_criteria"}

// Migration 164 adds the mission goal tables. It is additive: a database at the 163 head keeps its rows, the new tables
// are empty and a second run changes nothing.
func TestMigration164AddsTheMissionGoalTablesAdditively(t *testing.T) {
	db := open156(t)
	applyMigrationsBelow(t, db, 164)
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		var kind string
		if err := db.DB.QueryRow(`SELECT kind FROM task_sessions WHERE profile_id = 'p'`).Scan(&kind); err != nil || kind != "mission" {
			t.Fatalf("the session row after the upgrade: %q %v", kind, err)
		}
		for _, tb := range missionGoalTables {
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s: %d rows, %v", tb, n, err)
			}
		}
	}
	var v int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 164`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("164 recorded %d times, %v", v, err)
	}
}
