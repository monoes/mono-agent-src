package coderhost

import (
	"errors"
	"strings"
	"testing"
)

// A storage error while carving is the storage's, not a full limit: the task is not parked as waiting and the
// real error comes back.
func TestACarveStorageErrorIsNotAWait(t *testing.T) {
	f := carveRefusedFix(t)
	f.st.afterChildCreated = func() {
		if _, err := f.st.db.Exec(`CREATE TRIGGER fail_carve BEFORE UPDATE OF budget_usd ON coder_sessions
			WHEN NEW.budget_usd = 3 BEGIN SELECT RAISE(ABORT, 'disk on fire'); END`); err != nil {
			t.Error(err)
		}
	}
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	if err == nil || !strings.Contains(err.Error(), "disk on fire") {
		t.Fatalf("err = %v, want the storage error", err)
	}
	if errors.Is(err, ErrChildrenFull) || errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v: a storage error reads as a full limit", err)
	}
	if r := f.row(t, 0); r.State == ChildSpawning || r.State == ChildMarked && r.Attempts == 0 {
		t.Fatalf("row = %+v: the failure was treated as a refusal that costs nothing", r)
	}
}
