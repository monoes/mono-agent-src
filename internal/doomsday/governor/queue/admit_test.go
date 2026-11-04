package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func TestAdmitMovesOneRowOnceAndTheRowStillVerifies(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	id := enq(t, q, "a", governor.ClassNormal, "ref")
	ctx := context.Background()
	if err := q.MarkAdmitted(ctx, "p", 1, id); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkAdmitted(ctx, "p", 1, id); !errors.Is(err, governor.ErrNotQueued) {
		t.Fatalf("second admit: %v", err)
	}
	if err := q.MarkAdmitted(ctx, "p", 1, 999); !errors.Is(err, governor.ErrNotQueued) {
		t.Fatalf("unknown id: %v", err)
	}
	r, err := scanRow(db.QueryRow(`SELECT `+selectCols+` FROM doomsday_queue WHERE qid = ?`, id))
	if err != nil || r.State != governor.QueueAdmitted || !q.verify(r) {
		t.Fatalf("admitted row: %+v %v", r, err)
	}
	if rows, _, _ := q.Depth(ctx, "p", 1); rows != 0 {
		t.Fatal("admitted row still pending")
	}
}

// Each field the MAC covers: changing it with the guard trigger out of the way makes the row unadmittable.
func TestATamperedRowIsNeverAdmittedAndIsCancelled(t *testing.T) {
	cases := map[string]string{
		"lead":        `UPDATE doomsday_queue SET lead = 'evil' WHERE qid = ?`,
		"class":       `UPDATE doomsday_queue SET class = 'boss' WHERE qid = ?`,
		"enq_at":      `UPDATE doomsday_queue SET enq_at = enq_at - 1000 WHERE qid = ?`,
		"payload_sha": `UPDATE doomsday_queue SET payload_sha = zeroblob(32) WHERE qid = ?`,
		"payload":     `UPDATE doomsday_queue SET payload = 'swapped!' WHERE qid = ?`,
		"bytes":       `UPDATE doomsday_queue SET bytes = 3 WHERE qid = ?`,
		"mac":         `UPDATE doomsday_queue SET mac = x'01' WHERE qid = ?`,
	}
	for name, upd := range cases {
		t.Run(name, func(t *testing.T) {
			db := openDB(t)
			seedRun(t, db, "p", 1, `{}`)
			q := newQueue(t, db)
			bad := enq(t, q, "a", governor.ClassNormal, "payload1")
			good := enq(t, q, "b", governor.ClassNormal, "payload2")
			exec(t, db, `DROP TRIGGER doomsday_queue_forward_only`)
			exec(t, db, upd, bad)
			ctx := context.Background()
			if ids := peekIDs(t, q, 10); len(ids) != 1 || ids[0] != good {
				t.Fatalf("peek served the tampered row: %v", ids)
			}
			if err := q.MarkAdmitted(ctx, "p", 1, bad); !errors.Is(err, ErrRowTampered) {
				t.Fatalf("tampered row: %v", err)
			}
			if s := states(t, db)[bad]; s != "cancelled" {
				t.Fatalf("tampered row is %s", s)
			}
			if err := q.MarkAdmitted(ctx, "p", 1, good); err != nil {
				t.Fatalf("an untouched row was refused: %v", err)
			}
		})
	}
}

// A coordinated edit that keeps the payload hash and byte count consistent is caught by the MAC alone.
func TestConsistentEditOfPayloadAndHashIsCaughtByTheMACAlone(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	id := enq(t, q, "a", governor.ClassNormal, "payload1")
	exec(t, db, `DROP TRIGGER doomsday_queue_forward_only`)
	exec(t, db, `UPDATE doomsday_queue SET lead = 'other' WHERE qid = ?`, id)
	r, _ := scanRow(db.QueryRow(`SELECT `+selectCols+` FROM doomsday_queue WHERE qid = ?`, id))
	if !q.payloadMatches(r) {
		t.Fatal("setup: payload should still match its hash")
	}
	if err := q.MarkAdmitted(context.Background(), "p", 1, id); !errors.Is(err, ErrRowTampered) {
		t.Fatalf("%v", err)
	}
}

func TestCrashMidAdmitLeavesTheRowQueued(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	id := enq(t, q, "a", governor.ClassNormal, "ref")
	q.beforeCommit = func() error { return errors.New("simulated failure before commit") }
	if err := q.MarkAdmitted(context.Background(), "p", 1, id); err == nil {
		t.Fatal("expected the failure")
	}
	if states(t, db)[id] != "queued" {
		t.Fatal("a failed admission changed the row")
	}
	q.beforeCommit = func() error { panic("process died") }
	func() {
		defer func() { _ = recover() }()
		_ = q.MarkAdmitted(context.Background(), "p", 1, id)
	}()
	if states(t, db)[id] != "queued" {
		t.Fatal("a panic mid-admit changed the row")
	}
	q.beforeCommit = nil
	if err := q.MarkAdmitted(context.Background(), "p", 1, id); err != nil {
		t.Fatalf("the connection was left unusable: %v", err)
	}
}

func TestCancelAllWorksAfterStopAndAdmissionDoesNot(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	a, b := enq(t, q, "a", governor.ClassNormal, "1"), enq(t, q, "b", governor.ClassBoss, "2")
	ctx := context.Background()
	exec(t, db, `UPDATE doomsday_runs SET state = 'stop-requested' WHERE profile = 'p'`)
	if err := q.MarkAdmitted(ctx, "p", 1, a); !errors.Is(err, governor.ErrNotRunning) {
		t.Fatalf("admit after stop: %v", err)
	}
	if _, err := q.Enqueue(ctx, lim(0, 0), governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte("x")}); !errors.Is(err, governor.ErrNotRunning) {
		t.Fatalf("enqueue after stop: %v", err)
	}
	n, err := q.CancelAll(ctx, "p", 1)
	if err != nil || n != 2 {
		t.Fatalf("cancel: %d %v", n, err)
	}
	if st := states(t, db); st[a] != "cancelled" || st[b] != "cancelled" {
		t.Fatalf("%v", st)
	}
	if rows, bytes, _ := q.Depth(ctx, "p", 1); rows != 0 || bytes != 0 {
		t.Fatal("depth after cancel")
	}
	if n, err := q.CancelAll(ctx, "p", 1); err != nil || n != 0 {
		t.Fatalf("second cancel: %d %v", n, err)
	}
	r, _ := scanRow(db.QueryRow(`SELECT `+selectCols+` FROM doomsday_queue WHERE qid = ?`, a))
	if r.Payload != "" || !q.verify(r) {
		t.Fatalf("cancelled row keeps payload or does not verify: %+v", r)
	}
}

func TestCancelAllIsScopedToOneRun(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	seedRun(t, db, "p2", 1, `{}`)
	q := newQueue(t, db)
	enq(t, q, "a", governor.ClassNormal, "1")
	other, _ := q.Enqueue(context.Background(), governor.Limits{Profile: "p2", Generation: 1, MaxConcurrent: 1}, governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte("2")})
	if _, err := q.CancelAll(context.Background(), "p", 1); err != nil {
		t.Fatal(err)
	}
	if states(t, db)[other] != "queued" {
		t.Fatal("another run's row was cancelled")
	}
}

func TestCancelAllNeverMakesATamperedRowLookSound(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	bad := enq(t, q, "a", governor.ClassNormal, "1")
	enq(t, q, "b", governor.ClassNormal, "2")
	exec(t, db, `DROP TRIGGER doomsday_queue_forward_only`)
	exec(t, db, `UPDATE doomsday_queue SET lead = 'evil' WHERE qid = ?`, bad)
	if n, err := q.CancelAll(context.Background(), "p", 1); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	r, _ := scanRow(db.QueryRow(`SELECT `+selectCols+` FROM doomsday_queue WHERE qid = ?`, bad))
	if r.State != governor.QueueCancelled || q.verify(r) {
		t.Fatalf("tampered row cancelled=%v verifies=%v", r.State == governor.QueueCancelled, q.verify(r))
	}
}
