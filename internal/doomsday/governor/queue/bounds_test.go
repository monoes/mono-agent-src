package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func TestRowBoundRefusesTheNextEnqueueAndWritesNothing(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"queue_max_rows":3,"queue_max_bytes":1000}`)
	q := newQueue(t, db)
	for i := 0; i < 3; i++ {
		enq(t, q, "a", governor.ClassNormal, "ref")
	}
	_, err := q.Enqueue(context.Background(), lim(0, 0), governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte("ref")})
	if !errors.Is(err, governor.ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	if rows, _, _ := q.Depth(context.Background(), "p", 1); rows != 3 {
		t.Fatalf("depth %d, the refusal wrote something", rows)
	}
}

func TestByteBoundRefusesAndAnOversizedPayloadIsRefusedBeforeAnyWrite(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"queue_max_rows":100,"queue_max_bytes":10}`)
	q := newQueue(t, db)
	enq(t, q, "a", governor.ClassNormal, "123456")
	_, err := q.Enqueue(context.Background(), lim(0, 0), governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte("12345")})
	if !errors.Is(err, governor.ErrQueueFull) || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("the byte bound did not refuse: %v", err)
	}
	// Larger than the whole bound (Limits say 10): refused without touching the table.
	_, err = q.Enqueue(context.Background(), lim(0, 10), governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte(strings.Repeat("x", 11))})
	if !errors.Is(err, governor.ErrQueueFull) {
		t.Fatalf("oversized payload: %v", err)
	}
	if rows, b, _ := q.Depth(context.Background(), "p", 1); rows != 1 || b != 6 {
		t.Fatalf("depth %d rows %d bytes", rows, b)
	}
}

func TestHostileEnqueuersCannotExceedTheBoundsEvenInParallel(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"queue_max_rows":7,"queue_max_bytes":100000}`)
	q := newQueue(t, db)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, full := 0, 0
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := q.Enqueue(context.Background(), lim(0, 0), governor.Item{Lead: "hostile", Class: governor.ClassBoss, Payload: []byte("r")})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, governor.ErrQueueFull):
				full++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 7 || full != 23 {
		t.Fatalf("admitted %d refused %d", ok, full)
	}
}

// The bounds hold under a writer that skips this package (the triggers), including a payload lying about its size.
func TestTheDatabaseRefusesWhatThePackageWouldNotWrite(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"queue_max_rows":2,"queue_max_bytes":10}`)
	ins := `INSERT INTO doomsday_queue(profile,generation,lead,class,bytes,payload,payload_sha,state,enq_at,mac) VALUES ('p',1,'l','normal',?,?,zeroblob(32),?,1,x'01')`
	if _, err := db.Exec(ins, 1, "abcdefgh", "queued"); err == nil || !strings.Contains(err.Error(), "payload size") {
		t.Fatalf("a payload lying about its bytes was accepted: %v", err)
	}
	if _, err := db.Exec(ins, 8, "abcdefgh", "admitted"); err == nil {
		t.Fatal("a row born admitted was accepted")
	}
	if _, err := db.Exec(ins, 11, strings.Repeat("a", 11), "queued"); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("byte bound: %v", err)
	}
	exec(t, db, ins, 5, "abcde", "queued")
	exec(t, db, ins, 5, "abcde", "queued")
	if _, err := db.Exec(ins, 1, "a", "queued"); err == nil {
		t.Fatal("the byte bound allowed more than the charter")
	}
}

func TestEnqueueRefusalsBeforeTheWrite(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{}`)
	q := newQueue(t, db)
	ctx := context.Background()
	for name, it := range map[string]governor.Item{
		"no lead":    {Class: governor.ClassNormal, Payload: []byte("x")},
		"bad class":  {Lead: "a", Class: "vip", Payload: []byte("x")},
		"no payload": {Lead: "a", Class: governor.ClassNormal},
		"nul":        {Lead: "a", Class: governor.ClassNormal, Payload: []byte("a\x00b")},
		"not utf8":   {Lead: "a", Class: governor.ClassNormal, Payload: []byte{0xff, 0xfe}},
	} {
		if _, err := q.Enqueue(ctx, lim(0, 0), it); !errors.Is(err, ErrInvalidItem) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A run that does not exist or is not running.
	if _, err := q.Enqueue(ctx, governor.Limits{Profile: "p", Generation: 9, MaxConcurrent: 1}, governor.Item{Lead: "a", Class: governor.ClassNormal, Payload: []byte("x")}); !errors.Is(err, governor.ErrNotRunning) {
		t.Fatalf("no run: %v", err)
	}
	if rows, _, _ := q.Depth(ctx, "p", 1); rows != 0 {
		t.Fatal("a refusal wrote a row")
	}
}
