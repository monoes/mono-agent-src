package storage

import (
	"fmt"
	"testing"
)

var missionPursuitStateTables = []string{"mission_pursuit_state"}

// Migration 167 adds the mission pursuit state table. It is additive: a database at the 166 head, or still at 165, keeps its rows,
// the new table is empty and a second run changes nothing.
func TestMigration167AddsTheMissionPursuitStateTableAdditively(t *testing.T) {
	for _, below := range []int{167, 166} { // the 166 head, then the 165 head
		t.Run(fmt.Sprintf("from the head below %d", below), func(t *testing.T) { migration167From(t, below) })
	}
}

func migration167From(t *testing.T, below int) {
	db := open156(t)
	applyMigrationsBelow(t, db, below)
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
		for _, tb := range missionPursuitStateTables {
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s: %d rows, %v", tb, n, err)
			}
		}
	}
	for _, ver := range []int{166, 167} {
		var v int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, ver).Scan(&v); err != nil || v != 1 {
			t.Fatalf("%d recorded %d times, %v", ver, v, err)
		}
	}
}
