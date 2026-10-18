package storage

import (
	"path/filepath"
	"testing"
	"time"
)

// Close waits for the knowledge-graph syncs still running: they read the
// database it is about to close.
func TestCloseWaitsForRunningKGSyncs(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "kg.db"))
	if err != nil {
		t.Fatal(err)
	}
	release, finished := make(chan struct{}), make(chan struct{})
	db.kg.start(func() {
		<-release
		close(finished)
	})
	closed := make(chan struct{})
	go func() {
		db.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a sync was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-closed
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the sync finished")
	}
}

// A sync that does not finish holds Close for the limit and no longer, and
// nothing starts once closing has begun.
func TestKGSyncStopIsBoundedAndFinal(t *testing.T) {
	var k kgSyncs
	block := make(chan struct{})
	defer close(block)
	k.start(func() { <-block })
	start := time.Now()
	if k.stop(50 * time.Millisecond) {
		t.Fatal("stop reported every sync done while one was blocked")
	}
	if waited := time.Since(start); waited < 50*time.Millisecond || waited > 5*time.Second {
		t.Fatalf("stop waited %s for a 50ms limit", waited)
	}
	ran := make(chan struct{}, 1)
	k.start(func() { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("a sync started after closing began")
	case <-time.After(50 * time.Millisecond):
	}
}
