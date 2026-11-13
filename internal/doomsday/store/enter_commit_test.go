package store

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// onTripped returns a store over the same file whose driver can fail the COMMIT (#1460): the base store reads what
// really reached the file.
func onTripped(t *testing.T, s *SQLStore) (*SQLStore, *dbconntest.Trip) {
	t.Helper()
	var path string
	if err := s.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil || path == "" {
		t.Fatalf("database file: %q, %v", path, err)
	}
	db, trip := dbconntest.OpenPath(t, path)
	return New(db, testKeys()), trip
}

func rowCount(t *testing.T, s *SQLStore, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A COMMIT that lands and then reports an error is the outcome the caller cannot read from the error: Enter says
// ErrCommitUnknown (never a plain error, never success), the run is durable, and entering again is safe: the
// generation is taken, so nothing is recorded twice.
func TestEnterCommitThatLandedButReportedAnErrorIsUnknownAndRetrySafe(t *testing.T) {
	base, _ := newStore(t)
	w, trip := onTripped(t, base)
	c := charter("p1", 1)
	trip.CommitErrAfter = errors.New("connection lost after commit")
	run, err := w.Enter(ctx, c, c.Hash(), "operator")
	trip.CommitErrAfter = nil
	if !errors.Is(err, ErrCommitUnknown) || run != (Run{}) {
		t.Fatalf("run=%+v err=%v, want ErrCommitUnknown and no run", run, err)
	}
	got, err := base.GetRun(ctx, "p1", 1)
	if err != nil || got.CharterID != c.ID() || got.State != StateRunning {
		t.Fatalf("the committed run was not readable: %+v %v", got, err)
	}
	if _, err := w.Enter(ctx, c, c.Hash(), "operator"); !errors.Is(err, ErrGenerationMoved) || errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("a retry of an entry that committed: %v, want ErrGenerationMoved", err)
	}
	for _, tbl := range []string{"doomsday_charters", "doomsday_runs", "doomsday_run_seen"} {
		if n := rowCount(t, base, tbl); n != 1 {
			t.Fatalf("%s has %d rows after the retry, want 1", tbl, n)
		}
	}
}

// A COMMIT the engine refused stores nothing, and is still reported as unknown (the caller reads the run to find out).
func TestEnterCommitRefusedIsReportedAsUnknownAndStoresNothing(t *testing.T) {
	base, _ := newStore(t)
	w, trip := onTripped(t, base)
	c := charter("p1", 1)
	trip.CommitErr = errors.New("disk I/O error")
	run, err := w.Enter(ctx, c, c.Hash(), "operator")
	trip.CommitErr = nil
	if !errors.Is(err, ErrCommitUnknown) || run != (Run{}) {
		t.Fatalf("run=%+v err=%v, want ErrCommitUnknown and no run", run, err)
	}
	for _, tbl := range []string{"doomsday_charters", "doomsday_runs", "doomsday_run_seen", "doomsday_audit"} {
		if n := rowCount(t, base, tbl); n != 0 {
			t.Fatalf("%s has %d rows after a refused COMMIT, want 0", tbl, n)
		}
	}
}

// A refusal before the COMMIT is a plain refusal, not an unknown outcome.
func TestEnterRefusalsAreNotUnknown(t *testing.T) {
	base, _ := newStore(t)
	w, trip := onTripped(t, base)
	trip.CommitErrAfter = errors.New("must never be reached")
	c := charter("p1", 2) // the next generation is 1
	if _, err := w.Enter(ctx, c, c.Hash(), "operator"); !errors.Is(err, ErrGenerationMoved) || errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("err = %v, want ErrGenerationMoved", err)
	}
}

// The generation is read before the charter is written: a writer that commits in between must not fail the entry
// with SQLITE_BUSY_SNAPSHOT (#1460).
func TestEnterSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	s, _ := newStore(t)
	w, fired := onWrapped(t, s)
	c := charter("p1", 1)
	if _, err := w.Enter(ctx, c, c.Hash(), "operator"); err != nil {
		t.Fatalf("Enter failed when a writer committed between its read and its write: %v", err)
	}
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

// A COMMIT the engine refused must not leave its connection inside the entry's transaction: the next entry runs
// on the pool as it is and records the run.
func TestEnterAfterARefusedCommitStillRecordsTheRun(t *testing.T) {
	base, _ := newStore(t)
	w, trip := onTripped(t, base)
	c := charter("p1", 1)
	trip.CommitErr = errors.New("disk I/O error")
	if _, err := w.Enter(ctx, c, c.Hash(), "operator"); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("err = %v, want ErrCommitUnknown", err)
	}
	trip.CommitErr = nil
	r, err := w.Enter(ctx, c, c.Hash(), "operator")
	if err != nil {
		t.Fatalf("the retry after a refused COMMIT: %v", err)
	}
	if got, err := base.GetRun(ctx, "p1", 1); err != nil || got != r {
		t.Fatalf("stored run %+v %v, want %+v", got, err, r)
	}
}
