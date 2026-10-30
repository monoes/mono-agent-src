package coderhost

import (
	"errors"
	"testing"
)

// A refused runtime is not an accepted spawn: it does not use one of the spawn slots.
func TestARefusedRuntimeDoesNotUseASpawnSlot(t *testing.T) {
	f := newMC(t, true)
	r := req("A", f.bigScope())
	r.Runtime = "codex"
	for i := 0; i < SpawnRateMax+2; i++ {
		if _, err := f.spawnSession(r); !errors.Is(err, ErrAccess) {
			t.Fatalf("attempt %d: %v, want the runtime refusal", i, err)
		}
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_session_spawns`); n != 0 {
		t.Errorf("%d spawn slots used by refused requests", n)
	}
}
