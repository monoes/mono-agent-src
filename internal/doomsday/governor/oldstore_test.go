package governor

import (
	"context"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The store written before the governor existed keeps working on a database that has migration 155, and a charter it
// wrote (with no queue or safe-minimum fields) gets the governor's defaults from the triggers.
func TestTheStoreWithoutTheGovernorStillRunsAndItsCharterGetsTheDefaults(t *testing.T) {
	keyring.MockInit()
	db := openDB(t)
	s := store.New(db, store.NewHMACKeyer([]byte("k")))
	ctx := context.Background()
	c := store.Charter{
		Profile: "p", Generation: 1, Goal: "g",
		Limits:  store.Limits{TreeDepth: 4, LivePerTree: 2, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 2, MaxTotal: 100, BudgetMicro: 1},
		BossPin: store.Pin{Provider: "a", Model: "m1", Version: "v", PurposePolicy: "j"}, VerifierPin: store.Pin{Provider: "a", Model: "m2", Version: "v", PurposePolicy: "j"},
	}
	_, err := s.Enter(ctx, c, c.Hash(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	// The stored charter's own numbers are what the slot trigger enforces.
	for i, m := range []string{"m1", "m2", "m3"} {
		err := slot(db, "process", "t", m)
		if (i < 2) != (err == nil) {
			t.Fatalf("process slot %d: %v (cap 2)", i, err)
		}
	}
	if err := enqueue(db, "p", "normal", "x"); err != nil {
		t.Fatalf("default queue bound on an old charter: %v", err)
	}
	// Stop proceeds through every state: slots and queue rows never stand in the way of the old state machine.
	for _, to := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending, store.StateQuiescent} {
		if _, err = s.Advance(ctx, "p", 1, to, "operator", ""); err != nil {
			t.Fatalf("advance to %s: %v", to, err)
		}
	}
	if err := slot(db, "process", "t", "late"); err == nil {
		t.Fatal("a slot was reserved in a quiescent run")
	}
}
