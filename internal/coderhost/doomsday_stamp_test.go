package coderhost

import "testing"

func TestStampDoomsdayRunWritesTheCurrentTurnsSessionOnly(t *testing.T) {
	st := newStoreT(t)
	x, err := st.EnsureSession("p", "c1")
	if err != nil {
		t.Fatal(err)
	}
	x.State, x.TurnID = StateRunning, "t1"
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	if err := st.StampDoomsdayRun("p", "c1", "t1", "charter-9", 4); err != nil {
		t.Fatal(err)
	}
	var gen int64
	var charter string
	if err := st.db.QueryRow(`SELECT doomsday_generation, doomsday_charter_id FROM coder_sessions WHERE profile_id = 'p' AND conversation_id = 'c1'`).Scan(&gen, &charter); err != nil || gen != 4 || charter != "charter-9" {
		t.Fatalf("stamped %d %q (%v)", gen, charter, err)
	}
	if err := st.StampDoomsdayRun("p", "c1", "t-old", "charter-0", 1); err == nil {
		t.Error("a stale turn stamped the session")
	}
}
