package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/storage"
)

type syncLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncLog) logf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, f)
}

func (l *syncLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// The host sweeps the retained v2 rows at its start and then on its interval: a token row long past its expiry and the
// retention window is removed, a live one is not, and the loop ends with its context.
func TestTheHostSweepsRetainedRowsAtStartAndOnItsInterval(t *testing.T) {
	db, err := storage.NewDatabase(newChatCLITestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -30).Truncate(time.Millisecond)
	live := time.Now().UTC().Truncate(time.Millisecond)
	if err := procbind.InsertMissionToken(ctx, db.DB, key, procbind.MissionTokenRow{ID: "old", ProfileID: "default", TokenHash: "h1", Owner: "o", Session: 1, Task: 1, Role: "w", Actor: "a", CreatedAt: old, ExpiresAt: old.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := procbind.InsertMissionToken(ctx, db.DB, key, procbind.MissionTokenRow{ID: "live", ProfileID: "default", TokenHash: "h2", Owner: "o", Session: 1, Task: 1, Role: "w", Actor: "a", CreatedAt: live, ExpiresAt: live.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var lg syncLog
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); purgeRetainedEvery(cctx, db.DB, lg.logf, 20*time.Millisecond) }()
	count := func() (n int) {
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens_v2`).Scan(&n)
		return
	}
	for i := 0; i < 300 && count() != 1; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if count() != 1 {
		t.Errorf("%d token rows remain, want only the live one", count())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep loop did not end with its context")
	}
	var live1 int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens_v2 WHERE id = 'live'`).Scan(&live1); err != nil || live1 != 1 {
		t.Errorf("the live token was swept: %d %v", live1, err)
	}
}

// A failing sweep is logged and the loop carries on (the host start is never failed by it).
func TestAFailingSweepIsLoggedAndTheLoopCarriesOn(t *testing.T) {
	db, err := storage.NewDatabase(newChatCLITestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // every query now fails
	var lg syncLog
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); purgeRetainedEvery(ctx, db.DB, lg.logf, 20*time.Millisecond) }()
	for i := 0; i < 300 && !strings.Contains(lg.text(), "stopped early"); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(lg.text(), "stopped early") {
		t.Error("the failure was not logged")
	}
	time.Sleep(100 * time.Millisecond) // it is still looping: more than one failure
	cancel()
	<-done
	if strings.Count(lg.text(), "stopped early") < 2 {
		t.Errorf("the loop did not carry on after a failure: %q", lg.text())
	}
}
