package coderhost

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
)

// competeOnce moves the fixture's store onto a pool over the same file whose driver lets one competing writer commit
// right before the first statement the store executes from now on (the write of a read-then-write transaction).
// Where the transaction holds no write lock yet the writer commits and the transaction's write fails with
// SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for); where it holds the lock the writer is refused.
func (f *mcFix) competeOnce(t *testing.T) (fired func() bool) {
	t.Helper()
	var path string
	if err := f.st.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil || path == "" {
		t.Fatalf("database file: %q, %v", path, err)
	}
	db, trip := dbconntest.OpenPath(t, path)
	f.st.db = db
	competing := dbconntest.CompetingWriter(t, path)
	done := false
	trip.BeforeExec = func(string) {
		if !done {
			done = true
			competing()
		}
	}
	return func() bool { return done }
}

func notLocked(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("%s failed when a writer committed between its read and its write: %v", what, err)
	}
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// The write lock is taken before the first read of these three transactions (#1460).
func TestRequestBudgetSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	f, _ := budgetFix(t)
	fired := f.competeOnce(t)
	_, err := f.st.RequestBudget(f.scope(), "p", f.se.ID, TargetChild, f.ids[0], 2, "need more")
	notLocked(t, "RequestBudget", err)
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

func TestGrantBudgetSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	f, _ := budgetFix(t)
	b := f.ask(t, TargetChild, 2)
	fired := f.competeOnce(t)
	_, err := f.st.GrantBudget(OperatorScope{}, "p", b.ID, 0)
	notLocked(t, "GrantBudget", err)
	if !fired() {
		t.Fatal("the hook never ran")
	}
}

func TestReleaseReservationSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	f := newHostFix(t)
	f.mark(t, 0)
	row, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0])
	if err != nil {
		t.Fatal(err)
	}
	fired := f.competeOnce(t)
	err = f.st.releaseReservation("p", f.se.ID, f.ids[0], row.Generation, f.lead.ConversationID, false, "refused")
	notLocked(t, "releaseReservation", err)
	if !fired() {
		t.Fatal("the hook never ran")
	}
}
