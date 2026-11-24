package commsync

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func queueTwoNodes(t *testing.T, db *sql.DB, exec, ref string) {
	t.Helper()
	ctx := context.Background()
	db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status) VALUES (?, ?, 'w', 'hil', 'Approve', 'approved')`, ref, exec)
	Queue(ctx, db, Item{Profile: "default", HilRef: ref, Source: "x", Action: "post", Text: "t", Meta: map[string]interface{}{
		"workflow": map[string]interface{}{"target_nodes": []string{"n1", "n2"}}}})
	SetStatus(ctx, db, ref, Approved, Detail{})
}

// R16-09: an item aimed at two nodes is sent only when both finished, and a
// failure of the second is recorded rather than refused.
func TestMultiNodeItemIsSentOnlyWhenEveryNodeFinished(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	queueTwoNodes(t, db, "ex1", "a")
	NodeStarted(ctx, db, "ex1", "n1")
	if err := NodeFinished(ctx, db, "ex1", "n1", nil); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(t, db, "a"); s == "sent" {
		t.Fatalf("sent after the first of two nodes")
	}
	NodeStarted(ctx, db, "ex1", "n2")
	if err := NodeFinished(ctx, db, "ex1", "n2", errors.New("429")); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(t, db, "a"); s != "failed" {
		t.Fatalf("status = %s, want failed", s)
	}

	queueTwoNodes(t, db, "ex2", "b")
	for _, n := range []string{"n1", "n2"} {
		NodeStarted(ctx, db, "ex2", n)
		if err := NodeFinished(ctx, db, "ex2", n, nil); err != nil {
			t.Fatal(err)
		}
	}
	if s := statusOf(t, db, "b"); s != "sent" {
		t.Fatalf("status = %s, want sent", s)
	}
}
