package commsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

// sendingAt leaves the message of ref in 'sending' since the given time (status_at in its metadata).
func sendingAt(t *testing.T, db *sql.DB, ref string, since time.Time) {
	t.Helper()
	queueTargeted(t, db, "ex-"+ref, ref)
	NodeStarted(context.Background(), db, "ex-"+ref, "pub")
	raw, _ := json.Marshal(map[string]interface{}{"status_at": since.UTC().Format(time.RFC3339)})
	if _, err := db.Exec(`UPDATE person_messages SET metadata = ? WHERE hil_ref = ?`, string(raw), ref); err != nil {
		t.Fatal(err)
	}
}

func TestListStaleSending(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	now := time.Now()
	sendingAt(t, db, "old", now.Add(-2*time.Hour))
	sendingAt(t, db, "fresh", now.Add(-5*time.Minute))
	got, err := ListStaleSending(ctx, db, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].HilRef != "old" || got[0].Age < time.Hour {
		t.Fatalf("stale = %+v; want only the 2h-old row", got)
	}
}

func TestListStaleSendingNoRows(t *testing.T) {
	db := testDB(t)
	got, err := ListStaleSending(context.Background(), db, time.Now())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v; want nothing", got, err)
	}
}

func TestListStaleSendingChangesNothing(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	sendingAt(t, db, "old", time.Now().Add(-3*time.Hour))
	var before string
	db.QueryRow(`SELECT status || COALESCE(metadata,'') FROM person_messages WHERE hil_ref = 'old'`).Scan(&before)
	ListStaleSending(ctx, db, time.Now())
	var after string
	db.QueryRow(`SELECT status || COALESCE(metadata,'') FROM person_messages WHERE hil_ref = 'old'`).Scan(&after)
	if before != after || statusOf(t, db, "old") != "sending" {
		t.Fatalf("row changed: %q -> %q", before, after)
	}
}
