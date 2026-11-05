package store

import (
	"errors"
	"testing"
)

// The listing tells "could not list the runs" (nothing says a run exists) from "a run row is there and does not verify".
// Only the first is ErrRunsUnreadable; fail-open callers rely on that split, so a tampered row must never wear it.
func TestOpenRunsSplitsAnUnreadableTableFromAnUnverifiableRow(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := db.Exec(`DROP TRIGGER doomsday_runs_forward_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE doomsday_runs SET cause = 'forged' WHERE profile = 'p'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenRuns(ctx, "p"); err == nil || errors.Is(err, ErrRunsUnreadable) {
		t.Fatalf("a row that exists and does not verify must not be reported as an unreadable table: %v", err)
	}

	s2, db2 := newStore(t)
	if _, err := db2.Exec(`ALTER TABLE doomsday_runs RENAME TO moved_runs`); err != nil {
		t.Fatal(err)
	}
	_, err := s2.OpenRuns(ctx, "p")
	if !errors.Is(err, ErrRunsUnreadable) || !RunsTableMissing(err) {
		t.Fatalf("a missing table must be unreadable and missing: %v", err)
	}

	s3, db3 := newStore(t)
	db3.Close()
	_, err = s3.OpenRuns(ctx, "p")
	if !errors.Is(err, ErrRunsUnreadable) || RunsTableMissing(err) {
		t.Fatalf("a closed database must be unreadable and not missing: %v", err)
	}
}
