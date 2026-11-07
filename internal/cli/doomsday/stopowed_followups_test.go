package doomsday

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// Two repairs that read the same owed flag write the notice once: the flag is claimed (cleared) in the transaction that
// decides who writes, so the second finds nothing owed. (#1509)
func TestConcurrentRepairsWriteAnOwedNoticeOnce(t *testing.T) {
	r, ref, run1 := owedRig(t)
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	entered, gate := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	r.w.ChatNote = func(ctx context.Context, conv, msg string) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			close(entered)
			<-gate
		}
		return r.n.write(ctx, conv, msg)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.w.repairStop(context.Background(), time.Now()) }()
	<-entered
	r.w.repairStop(context.Background(), time.Now()) // the second repair, while the first holds its write
	close(gate)
	wg.Wait()
	if got := r.n.got["conv-a"]; len(got) != 1 {
		t.Fatalf("%d notices written, want 1: %v", len(got), got)
	}
}

// A repair that wrote its notice clears only the flag it honoured: a flag another run put there meanwhile stays. (#1509)
func TestARepairDoesNotClearAnotherRunsFlag(t *testing.T) {
	r, ref, run1 := owedRig(t)
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	other := coderhost.OwedStopNotice{Charter: "another-charter", Generation: 9, IntentAt: run1.IntentAt + 1, Code: owedPlain, At: run1.IntentAt + 1}
	r.w.ChatNote = func(ctx context.Context, conv, msg string) error {
		if err := r.setOwed(t, conv, other); err != nil {
			t.Error(err)
		}
		return r.n.write(ctx, conv, msg)
	}
	r.w.repairStop(context.Background(), time.Now())
	if f := r.owedFlag(t, "conv-a"); f == nil || f.Charter != other.Charter {
		t.Fatalf("another run's flag was cleared: %+v", f)
	}
}

// Past the window the sweep drops the flags of every earlier run too, not only the latest generation's. (#1509)
func TestExpiredSweepDropsFlagsOfEarlierRuns(t *testing.T) {
	r, ref, run1 := owedRig(t)
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	r.w.dropExpiredFlags(context.Background(), RunRef{CharterID: ref.CharterID, Generation: ref.Generation + 1})
	if f := r.owedFlag(t, "conv-a"); f != nil {
		t.Fatalf("a flag of an earlier run survived the sweep: %+v", f)
	}
}

// A flag write never creates the session row of a chat deleted meanwhile, and does not bump the chat's updated_at. (#1509)
func TestOwedFlagWritesCreateNoRowAndKeepUpdatedAt(t *testing.T) {
	r, ref, run1 := owedRig(t)
	o := owedChat{Code: owedPlain, At: run1.IntentAt}
	_ = r.w.writePending(ref, run1.IntentAt, "conv-deleted", o)
	var n int
	if err := r.w.DB.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE conversation_id = 'conv-deleted'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("an orphan session row was created: %d %v", n, err)
	}
	if err := r.setOwed(t, "conv-a", coderhost.OwedStopNotice{Charter: "x", Generation: 1, IntentAt: 1, Code: owedPlain, At: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.DB.Exec(`UPDATE coder_sessions SET updated_at = '2020-01-01T00:00:00Z' WHERE conversation_id = 'conv-a'`); err != nil {
		t.Fatal(err)
	}
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", o); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := r.w.DB.QueryRow(`SELECT updated_at FROM coder_sessions WHERE conversation_id = 'conv-a'`).Scan(&got); err != nil || got != "2020-01-01T00:00:00Z" {
		t.Fatalf("updated_at moved: %q %v", got, err)
	}
}

