package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func (f *mcFix) setDirections(t *testing.T, conv string, k int) {
	t.Helper()
	if _, err := f.st.UpdateSettings("p", conv, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		setMD(&cur, k)
		cur.MaxChildren = 12
		return cur, nil
	}); err != nil {
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

func TestMissionDirectionsIsTheOperatorsAndBoundedTo6(t *testing.T) {
	if !oneOf("mission_directions", OperatorOnly) {
		t.Fatal("mission_directions must be operator-only")
	}
	d := DefaultSettings()
	if d.MissionDirections != nil || d.EffectiveMissionDirections() != 3 {
		t.Fatalf("unset means the default of 3: %v", d.MissionDirections)
	}
	for _, k := range []string{"1", "3", "6"} {
		got, changed, err := d.Apply([]byte(`{"mission_directions":` + k + `}`))
		if err != nil || len(changed) != 1 || changed[0] != "mission_directions" || TouchesOperatorOnly(changed)[0] != "mission_directions" {
			t.Errorf("%s: %+v %v %v", k, got, changed, err)
		}
	}
	for _, k := range []string{"7", "-1", "100", `"3"`, "2.5"} {
		if _, _, err := d.Apply([]byte(`{"mission_directions":` + k + `}`)); err == nil {
			t.Errorf("mission_directions=%s was accepted", k)
		}
	}
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
	setMD(&parent, 5)
	for _, lvl := range []int{1, 2, 4} {
		if got, ok := mdOf(childSettings(parent, lvl)); !ok || got != 5 {
			t.Fatalf("level %d child has mission_directions %d, want 5", lvl, got)
		}
	}
}

// An archived draft holds no place: the next spawn passes. A direction the operator closed holds none either.
func TestArchivedAndClosedDirectionsFreeTheirSlot(t *testing.T) {
	f := newMC(t, true)
	f.setDirections(t, f.lead.ConversationID, 1)
	ctx := context.Background()
	first, err := f.spawnDirection(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.spawnDirection(1); !errors.Is(err, ErrLimit) {
		t.Fatalf("the place is taken: %v", err)
	}
	if _, err := f.ts.ArchiveSession(ctx, "p", first.TaskSessionID, opActor); err != nil {
		t.Fatalf("archiving a draft: %v", err)
	}
	second, err := f.spawnDirection(2)
	if err != nil {
		t.Fatalf("an archived draft must free its slot: %v", err)
	}
	if _, err := f.ts.StopSession(ctx, "p", second.TaskSessionID, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.spawnDirection(3); err != nil {
		t.Fatalf("a stopped direction must free its slot: %v", err)
	}
}
