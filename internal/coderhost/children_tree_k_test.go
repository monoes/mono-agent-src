package coderhost

import (
	"context"
	"database/sql"
	"testing"
)

// spawnWithOperatorK spawns a child of a master whose K is parentK, and runs write (the operator's commit on the
// child) in the window between SetRole and the spawn's settings initialization. It returns the child's stored K.
func spawnWithOperatorK(t *testing.T, parentK int, write func(st *Store, child string)) Settings {
	t.Helper()
	st := newStoreT(t)
	c, _ := master(t, st, "coding")
	if _, err := st.UpdateSettings("p", c.ConversationID, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		setMD(&cur, parentK)
		return cur, nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := st.spawnWith(c, SpawnRequest{}, spawnHooks{onCreated: func(child string) error {
		if write != nil {
			write(st, child)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.UpdateSettings("p", out.ConversationID, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) { return cur, nil })
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func setK(k int) func(*Store, string) {
	return func(st *Store, child string) {
		if _, err := st.UpdateSettings("p", child, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
			setMD(&cur, k)
			return cur, nil
		}); err != nil {
			panic(err)
		}
	}
}

func TestSpawnInitKeepsAnOperatorKCommittedSinceSetRole(t *testing.T) {
	if got, ok := mdOf(spawnWithOperatorK(t, 6, setK(1))); !ok || got != 1 {
		t.Fatalf("child set to 1 ends at (%d, set=%v): the init overwrote the operator's K", got, ok)
	}
}

func TestSpawnInitKeepsAnExplicitZeroK(t *testing.T) {
	if got, ok := mdOf(spawnWithOperatorK(t, 6, setK(0))); !ok || got != 0 {
		t.Fatalf("an explicit 0 ends at (%d, set=%v), want an explicit 0", got, ok)
	}
}

func TestSpawnInitInheritsTheParentsKWhenTheChildHasNone(t *testing.T) {
	if got, ok := mdOf(spawnWithOperatorK(t, 5, nil)); !ok || got != 5 {
		t.Fatalf("child K = (%d, set=%v), want the parent's 5", got, ok)
	}
}
