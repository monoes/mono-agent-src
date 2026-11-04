package queue

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// probeKeys writes through a second handle on the same file from inside every MAC call: if the queue holds its write
// transaction while it computes MACs, that write is busy.
type probeKeys struct {
	inner governor.Keyer
	other *sql.DB
	mu    sync.Mutex
	busy  int
	tried int
	on    bool
}

func (k *probeKeys) MAC(d string, p ...[]byte) ([]byte, error) {
	k.mu.Lock()
	on := k.on
	k.mu.Unlock()
	if on {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		conn, err := k.other.Conn(ctx)
		if err == nil {
			_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
			if err == nil {
				_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			}
			conn.Close()
		}
		cancel()
		k.mu.Lock()
		k.tried++
		if err != nil {
			k.busy++
		}
		k.mu.Unlock()
	}
	return k.inner.MAC(d, p...)
}

// Stop's cancel must not hold the database write lock while it verifies rows (each verify can be a keychain read).
func TestCancelAllDoesNotHoldTheWriteLockWhileVerifying(t *testing.T) {
	path := filepath.Join(t.TempDir(), "q.db")
	db := openAt(t, path)
	other := openAt(t, path)
	seedRun(t, db, "p", 1, `{"max_concurrent":4}`)
	pk := &probeKeys{inner: store.NewHMACKeyer([]byte("queue-test-key")), other: other}
	q := New(db, pk)
	for i := 0; i < 5; i++ {
		enq(t, q, "l", governor.ClassNormal, "m"+string(rune('a'+i)))
	}
	pk.mu.Lock()
	pk.on = true
	pk.mu.Unlock()
	n, err := q.CancelAll(context.Background(), "p", 1)
	if err != nil || n != 5 {
		t.Fatalf("cancelled %d (%v)", n, err)
	}
	if pk.tried == 0 {
		t.Fatal("the probe never ran")
	}
	if pk.busy != 0 {
		t.Fatalf("%d of %d MACs ran while the write lock was held", pk.busy, pk.tried)
	}
	for id, st := range states(t, db) {
		if st != "cancelled" {
			t.Fatalf("row %d is %s", id, st)
		}
	}
}
