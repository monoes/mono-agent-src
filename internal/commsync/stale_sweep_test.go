package commsync

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A message stuck in 'sending' for more than StaleSendingGivesUpAfter whose
// execution is no longer running is ended as failed with an "outcome unknown"
// note. A younger one, one whose execution still runs, and any other status
// are left alone. Failed is terminal, so the sweep can never make a message
// sendable again.
func TestSweepStaleSendingEndsOnlyAbandonedOldRows(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	now := time.Now()
	old := now.Add(-StaleSendingGivesUpAfter - time.Hour)
	if _, err := db.Exec(`INSERT INTO workflows (id, name) VALUES ('w', 'w')`); err != nil {
		t.Fatal(err)
	}
	for ref, st := range map[string]string{"dead": "FAILED", "young": "FAILED", "live": "RUNNING", "orphan": "FAILED"} {
		if _, err := db.Exec(`INSERT INTO workflow_executions (id, workflow_id, status) VALUES (?, 'w', ?)`, "ex-"+ref, st); err != nil {
			t.Fatal(err)
		}
	}
	sendingAt(t, db, "dead", old)
	sendingAt(t, db, "young", now.Add(-StaleSendingAfter-time.Hour))
	sendingAt(t, db, "live", old)
	sendingAt(t, db, "orphan", old) // an agent send: its hil item is gone
	if _, err := db.Exec(`UPDATE person_messages SET hil_ref = 'agent-1' WHERE hil_ref = 'orphan'`); err != nil {
		t.Fatal(err)
	}

	n, err := SweepStaleSending(ctx, db, now)
	if err != nil || n != 2 {
		t.Fatalf("swept %d, %v; want the dead and the item-less rows", n, err)
	}
	for ref, want := range map[string]string{"dead": "failed", "agent-1": "failed", "young": "sending", "live": "sending"} {
		if got := statusOf(t, db, ref); got != want {
			t.Errorf("%s: %s, want %s", ref, got, want)
		}
	}
	var meta string
	db.QueryRow(`SELECT metadata FROM person_messages WHERE hil_ref = 'dead'`).Scan(&meta)
	if !strings.Contains(meta, "outcome unknown") {
		t.Errorf("no honest note: %s", meta)
	}
	if err := SetStatus(ctx, db, "dead", Approved, Detail{}); err == nil {
		t.Error("a swept message went back to approved")
	}
	if n, _ := SweepStaleSending(ctx, db, now); n != 0 {
		t.Errorf("second sweep changed %d rows", n)
	}
}
