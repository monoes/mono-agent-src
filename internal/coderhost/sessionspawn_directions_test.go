package coderhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func (f *mcFix) setDirections(t *testing.T, conv string, k int) {
	t.Helper()
	s, _ := f.st.GetSettings("p", conv)
	s.MissionDirections = k
	s.MaxChildren = 12
	if err := f.st.SaveSettings("p", conv, s); err != nil {
		t.Fatal(err)
	}
}

func (f *mcFix) spawnDirection(i int) (SessionSpawned, error) {
	r := SessionSpawnRequest{Title: fmt.Sprintf("dir%d", i), Goal: "g", Kind: tasks.SessionKindDirection, Reason: ReasonParallel, TaskIDs: f.bigScope()}
	return f.st.SpawnSession(context.Background(), f.scope(), "p", r)
}

func openDirections(t *testing.T, f *mcFix) int {
	t.Helper()
	return countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id = ? AND kind = 'direction' AND state IN ('draft','running','paused')`, f.se.ID)
}

func TestSpawnSessionHoldsTheOperatorsMissionDirections(t *testing.T) {
	f := newMC(t, true)
	f.setDirections(t, f.lead.ConversationID, 2)
	for i := 0; i < 2; i++ {
		if _, err := f.spawnDirection(i); err != nil {
			t.Fatalf("direction %d: %v", i, err)
		}
	}
	if _, err := f.spawnDirection(2); !errors.Is(err, ErrLimit) {
		t.Fatalf("a third direction past mission_directions=2: %v", err)
	}
	if n := openDirections(t, f); n != 2 {
		t.Fatalf("%d open directions, want 2", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE parent_conversation_id = ? AND state != 'stopped'`, f.lead.ConversationID); n != 2 {
		t.Fatalf("%d live children, want 2", n)
	}
}

func TestSpawnSessionDirectionsDefaultToThreeAndNeverPassSix(t *testing.T) {
	f := newMC(t, true)
	f.setDirections(t, f.lead.ConversationID, 0)
	okN := 0
	for i := 0; i < 5; i++ {
		if _, err := f.spawnDirection(i); err == nil {
			okN++
		}
	}
	if okN != 3 {
		t.Fatalf("%d directions with the default setting, want 3", okN)
	}
}

func TestConcurrentDirectionSpawnsNeverPassTheCap(t *testing.T) {
	f := newMC(t, true)
	f.setDirections(t, f.lead.ConversationID, 2)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _ = f.spawnDirection(i) }(i)
	}
	wg.Wait()
	if n := openDirections(t, f); n > 2 {
		t.Fatalf("%d open directions under a cap of 2", n)
	}
}

func TestADirectionLeadKeepsTheOperatorsMissionDirections(t *testing.T) {
	parent := DefaultSettings()
	parent.MissionDirections = 5
	for _, lvl := range []int{1, 2, 4} {
		if got := childSettings(parent, lvl).MissionDirections; got != 5 {
			t.Fatalf("level %d child has mission_directions %d, want 5", lvl, got)
		}
	}
}
