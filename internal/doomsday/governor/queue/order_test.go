package queue

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func TestBossClassIsServedFirstThenNormalFIFOWithinALead(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	n1 := enq(t, q, "a", governor.ClassNormal, "n1")
	n2 := enq(t, q, "a", governor.ClassNormal, "n2")
	b1 := enq(t, q, "a", governor.ClassBoss, "b1")
	b2 := enq(t, q, "b", governor.ClassBoss, "b2")
	if got, want := peekIDs(t, q, 10), []int64{b1, b2, n1, n2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v want %v", got, want)
	}
}

func TestNoLeadStarvesAnotherLeadsFirstItemWaitsOneRound(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"queue_max_rows":1000}`)
	q := newQueue(t, db)
	for i := 0; i < 50; i++ {
		enq(t, q, "flood", governor.ClassNormal, "f")
	}
	small := enq(t, q, "small", governor.ClassNormal, "s")
	third := enq(t, q, "third", governor.ClassNormal, "t")
	ids := peekIDs(t, q, 3)
	if len(ids) != 3 || ids[1] != small || ids[2] != third {
		t.Fatalf("a flooding lead delayed the others by more than a round: %v", ids)
	}
	// Serving: after admitting the flooder's head, the other leads come before its next item.
	if err := q.MarkAdmitted(context.Background(), "p", 1, ids[0]); err != nil {
		t.Fatal(err)
	}
	if next := peekIDs(t, q, 3); next[0] != small || next[1] != third {
		t.Fatalf("least served lead not first: %v", next)
	}
}

func TestRoundRobinAlternatesLeadsAndIsDeterministic(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	a1, a2, a3 := enq(t, q, "a", governor.ClassNormal, "1"), enq(t, q, "a", governor.ClassNormal, "2"), enq(t, q, "a", governor.ClassNormal, "3")
	b1, b2 := enq(t, q, "b", governor.ClassNormal, "1"), enq(t, q, "b", governor.ClassNormal, "2")
	want := []int64{a1, b1, a2, b2, a3}
	for i := 0; i < 3; i++ {
		if got := peekIDs(t, q, 10); !reflect.DeepEqual(got, want) {
			t.Fatalf("order %v want %v", got, want)
		}
	}
}

func TestPeekIsBoundedWhateverTheCallerAsks(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"queue_max_rows":1000}`)
	q := newQueue(t, db)
	for i := 0; i < MaxPeek+100; i++ {
		enq(t, q, "a", governor.ClassNormal, "x")
	}
	if n := len(peekIDs(t, q, 7)); n != 7 {
		t.Fatalf("peek(7) = %d", n)
	}
	if n := len(peekIDs(t, q, 1<<30)); n != MaxPeek {
		t.Fatalf("peek(huge) = %d, want the ceiling %d", n, MaxPeek)
	}
	if n := len(peekIDs(t, q, -1)) + len(peekIDs(t, q, 0)); n != 0 {
		t.Fatal("non-positive max returned items")
	}
}

func TestTheQueueSurvivesAReopenInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	db := openAt(t, path)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	a := enq(t, q, "a", governor.ClassNormal, "1")
	b := enq(t, q, "b", governor.ClassBoss, "2")
	c := enq(t, q, "a", governor.ClassNormal, "3")
	db.Close()
	q2 := newQueue(t, openAt(t, path))
	if got, want := peekIDs(t, q2, 10), []int64{b, a, c}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after reopen %v want %v", got, want)
	}
	items, _ := q2.Peek(context.Background(), "p", 1, 10)
	if string(items[0].Payload) != "2" || items[0].Lead != "b" || items[0].Class != governor.ClassBoss || items[0].EnqAt == 0 {
		t.Fatalf("item did not round trip: %+v", items[0])
	}
}
