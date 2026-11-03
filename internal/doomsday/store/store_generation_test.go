package store

import (
	"errors"
	"testing"
)

// A forged second running row for an older generation (the unique index removed) is still not admitted.
func TestAdmitRefusesNonCurrentGenerationEvenIfRunning(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	walk(t, s, r, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	enter(t, s, "p")
	if _, err := db.Exec(`DROP INDEX idx_doomsday_runs_one_active`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER doomsday_runs_forward_only`); err != nil {
		t.Fatal(err)
	}
	old := Run{Profile: "p", Generation: 1, CharterID: r.CharterID, State: StateRunning}
	mac, _ := s.runMAC(old)
	if _, err := db.Exec(`UPDATE doomsday_runs SET state = 'running', intent_at = 0, intent_by = '', cause = '', mac = ? WHERE generation = 1`, mac); err != nil {
		t.Fatal(err)
	}
	var ref *Refusal
	if err := s.Admit(ctx, db, "p", 1); !errors.As(err, &ref) {
		t.Fatalf("old generation admitted: %v", err)
	}
}
