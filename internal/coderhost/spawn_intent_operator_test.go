package coderhost

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The operator's end of a held spawn intent (spawn_intent_operator.go): only a needs-operator row, only with the
// generation `list` showed, either kept (one acceptance row) or discarded through the sweep's guarded delete.

// heldIntent leaves a needs-operator intent: a crashed spawn whose task session the operator approved.
func (f *mcFix) heldIntent(t *testing.T) SpawnIntentView {
	t.Helper()
	f.crashedSpawn(t)
	f.approveChildSession(t)
	f.afterLease(time.Minute)
	_, _ = f.st.SweepSpawnIntents(context.Background(), 20)
	vs, err := f.st.ListSpawnIntents("p")
	if err != nil || len(vs) != 1 || !vs[0].NeedsOperator {
		t.Fatalf("list = %+v, %v; want one needs-operator intent", vs, err)
	}
	return vs[0]
}

func TestListShowsTheHeldIntentWithItsGenerationAndPair(t *testing.T) {
	f := newMC(t, true)
	v := f.heldIntent(t)
	if v.Spawner != f.lead.ConversationID || v.State != "needs-operator" || v.Child == "" || v.Generation == 0 || len(v.TaskSessions) != 1 {
		t.Errorf("view = %+v", v)
	}
}

func TestResolveRefusesWhatIsNotHeldStaleOrUnknown(t *testing.T) {
	f := newMC(t, true)
	f.crashedSpawn(t) // pending
	var gen int64
	_ = f.st.db.QueryRow(`SELECT generation FROM coder_spawn_intents`).Scan(&gen)
	for _, keep := range []bool{true, false} {
		if err := f.st.ResolveSpawnIntent(context.Background(), "p", f.lead.ConversationID, gen, keep); !errors.Is(err, ErrSpawnIntentNotHeld) {
			t.Errorf("keep=%v pending: %v, want ErrSpawnIntentNotHeld", keep, err)
		}
		if err := f.st.ResolveSpawnIntent(context.Background(), "p", "nobody", 1, keep); !errors.Is(err, ErrSpawnIntentUnknown) {
			t.Errorf("keep=%v unknown: %v", keep, err)
		}
	}
	if _, err := f.st.db.Exec(`UPDATE coder_spawn_intents SET state = 'sweeping'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ResolveSpawnIntent(context.Background(), "p", f.lead.ConversationID, gen, true); !errors.Is(err, ErrSpawnIntentNotHeld) {
		t.Errorf("sweeping: %v", err)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents, the refusals must leave the row", n)
	}
	if n := acceptances(t, f); n != 0 {
		t.Errorf("%d acceptance rows after refusals", n)
	}
}

func TestResolveWithAStaleGenerationChangesNothing(t *testing.T) {
	f := newMC(t, true)
	v := f.heldIntent(t)
	for _, keep := range []bool{true, false} {
		if err := f.st.ResolveSpawnIntent(context.Background(), "p", v.Spawner, v.Generation-1, keep); !errors.Is(err, ErrSpawnIntentStale) {
			t.Errorf("keep=%v: %v, want ErrSpawnIntentStale", keep, err)
		}
	}
	f.wantPairKept(t)
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents", n)
	}
}

func TestKeepWritesOneAcceptanceRowDropsTheIntentAndLeavesThePair(t *testing.T) {
	f := newMC(t, true)
	v := f.heldIntent(t)
	if err := f.st.ResolveSpawnIntent(context.Background(), "p", v.Spawner, v.Generation, true); err != nil {
		t.Fatal(err)
	}
	if n := acceptances(t, f); n != 1 {
		t.Errorf("%d acceptance rows, want exactly 1", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents left", n)
	}
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations, keep leaves the child", n)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 1 {
		t.Errorf("%d open child task sessions, keep leaves the session", n)
	}
	if err := f.st.ResolveSpawnIntent(context.Background(), "p", v.Spawner, v.Generation, true); !errors.Is(err, ErrSpawnIntentUnknown) {
		t.Errorf("a second keep: %v", err)
	}
	if n := acceptances(t, f); n != 1 {
		t.Errorf("%d acceptance rows after a second keep", n)
	}
}

func TestDiscardRefusesAnApprovedPairAndRemovesADraftPair(t *testing.T) {
	f := newMC(t, true)
	v := f.heldIntent(t)
	if err := f.st.ResolveSpawnIntent(context.Background(), "p", v.Spawner, v.Generation, false); !errors.Is(err, ErrSpawnIntentLive) {
		t.Fatalf("discard of an approved pair: %v, want ErrSpawnIntentLive", err)
	}
	f.wantPairKept(t)
	if st, _ := f.intentState(t); st != "needs-operator" {
		t.Errorf("the intent is %q after the refusal", st)
	}

	g := newMC(t, true) // a held row whose pair is still a draft (five failed cleanups)
	g.crashedSpawn(t)
	if _, err := g.st.db.Exec(`UPDATE coder_spawn_intents SET state = 'needs-operator', generation = generation + 1`); err != nil {
		t.Fatal(err)
	}
	vs, _ := g.st.ListSpawnIntents("p")
	if len(vs) != 1 {
		t.Fatalf("list = %+v", vs)
	}
	if err := g.st.ResolveSpawnIntent(context.Background(), "p", vs[0].Spawner, vs[0].Generation, false); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, g.st, childConversations); n != 0 {
		t.Errorf("%d child conversations after discarding a draft pair", n)
	}
	if n := countRows(t, g.st, openChildTaskSessions); n != 0 {
		t.Errorf("%d open child task sessions", n)
	}
	if n := countRows(t, g.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents left", n)
	}
	if n := acceptances(t, g); n != 0 {
		t.Errorf("%d acceptance rows after a discard", n)
	}
}

func TestCountHeldSpawnIntentsCountsOnlyNeedsOperator(t *testing.T) {
	f := newMC(t, true)
	f.crashedSpawn(t)
	if n, err := f.st.CountHeldSpawnIntents(); err != nil || n != 0 {
		t.Errorf("pending: %d, %v", n, err)
	}
	f.approveChildSession(t)
	f.afterLease(time.Minute)
	_, _ = f.st.SweepSpawnIntents(context.Background(), 20)
	if n, err := f.st.CountHeldSpawnIntents(); err != nil || n != 1 {
		t.Errorf("held: %d, %v", n, err)
	}
}
