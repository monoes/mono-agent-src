package application

import (
	"database/sql"
	"testing"
)

// Recording a send closes the application's tab binding at once, so it frees its slot without waiting for a sweep (R10-15).
func TestSendClosesTheTabBinding(t *testing.T) {
	dbPath := homeDB(t)
	id := addJob(t, dbPath)
	withDB(t, dbPath, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO application_tabs(application_id, task_id, tab_id, origin, state, updated_at)
			VALUES (?, 't1', 7, 'https://acme.example', 'parked', '2026-01-01T00:00:00Z')`, id); err != nil {
			t.Fatal(err)
		}
	})
	if out, err := run(t, dbPath, true, "send", id); err != nil {
		t.Fatalf("send: %v (%s)", err, out)
	}
	withDB(t, dbPath, func(db *sql.DB) {
		var st string
		if err := db.QueryRow(`SELECT state FROM application_tabs WHERE application_id = ?`, id).Scan(&st); err != nil || st != "closed" {
			t.Fatalf("binding state = %q, %v; want closed", st, err)
		}
	})
}
