package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// One row is cancelled by id; a row that is not queued, or not this run's, is ErrNotQueued; the others stay queued.
func TestCancelOneRowLeavesTheOthersQueued(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":4}`)
	q := newQueue(t, db)
	a, b := enq(t, q, "l1", governor.ClassNormal, "a"), enq(t, q, "l2", governor.ClassNormal, "b")
	if err := q.Cancel(context.Background(), "p", 1, a); err != nil {
		t.Fatal(err)
	}
	if err := q.Cancel(context.Background(), "p", 1, a); !errors.Is(err, governor.ErrNotQueued) {
		t.Fatalf("second cancel: %v", err)
	}
	if err := q.Cancel(context.Background(), "other", 1, b); !errors.Is(err, governor.ErrNotQueued) {
		t.Fatalf("another profile's cancel: %v", err)
	}
	if got := states(t, db); got[a] != "cancelled" || got[b] != "queued" {
		t.Fatalf("%v", got)
	}
}
