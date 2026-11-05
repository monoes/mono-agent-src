package store

import "testing"

// Entering a run records, in the same transaction, that the profile has opened a run; nothing before that does.
func TestEnterRecordsThatARunWasOpenedAndNothingElseDoes(t *testing.T) {
	s, db := newStore(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_run_seen`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a fresh database must hold no record: %d %v", n, err)
	}
	enter(t, s, "p")
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_run_seen WHERE profile = 'p'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("entering a run must record it: %d %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_run_seen WHERE profile = 'other'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("another profile has no record: %d %v", n, err)
	}
}

// A refused entry leaves no extra record, and the record is not moved by a later run.
func TestARefusedEntryRecordsNothingMore(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	c := charter("p", 2)
	if _, err := s.Enter(ctx, c, c.Hash(), "operator"); err == nil {
		t.Fatal("a second active run was entered")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_run_seen`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("one profile, one record: %d %v", n, err)
	}
}

// The record is insert-only: it can be neither changed, removed nor replaced.
func TestRunSeenRecordIsInsertOnly(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	for name, q := range map[string]string{
		"update":  `UPDATE doomsday_run_seen SET first_generation = 9`,
		"remove":  `DELETE FROM doomsday_run_seen`,
		"replace": `INSERT OR REPLACE INTO doomsday_run_seen(profile, first_generation, first_at) VALUES ('p', 9, 1)`,
		"again":   `INSERT INTO doomsday_run_seen(profile, first_generation, first_at) VALUES ('p', 9, 1)`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	var n int
	var gen int64
	if err := db.QueryRow(`SELECT COUNT(*), MIN(first_generation) FROM doomsday_run_seen`).Scan(&n, &gen); err != nil || n != 1 || gen != 1 {
		t.Fatalf("the record changed: %d %d %v", n, gen, err)
	}
}
