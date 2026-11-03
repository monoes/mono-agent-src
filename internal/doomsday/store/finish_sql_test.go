package store

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// These tests call the conditional insert of the finish-commit directly, with the Go-side evaluation bypassed and the
// head check switched off (head 0), so each clause of the statement is proven to hold on its own: dropping one lets the
// row in and its test fails.

func (f *finFix) commitReady(t *testing.T) {
	t.Helper()
	f.freeze(t, "d")
	f.ready(t, f.verifier(finish.VerdictOK))
	if err := f.l.RecordRecheck(ctx, true); err != nil {
		t.Fatal(err)
	}
}

// attempt runs insertCommit inside a transaction that is always rolled back. before may add rows to the same
// transaction; clearHead removes the head check so the SQL conditions are the only thing under test.
func (f *finFix) attempt(t *testing.T, clearHead bool, before func(tx *sql.Tx)) error {
	t.Helper()
	tx, err := f.s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	v, err := f.s.loadFinishView(ctx, tx, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if before != nil {
		before(tx)
	}
	if clearHead {
		v.head = 0
	}
	return f.l.insertCommit(ctx, tx, v)
}

func (f *finFix) raw(t *testing.T, tx *sql.Tx, kind string) {
	t.Helper()
	if _, ok, err := f.s.appendAuditIf(ctx, tx, "p", 1, kind, "{}", 0, ""); err != nil || !ok {
		t.Fatalf("%s: %v %v", kind, ok, err)
	}
}

func TestFinishCommitStatementClauses(t *testing.T) {
	f := newFin(t)
	f.commitReady(t)
	if err := f.attempt(t, false, nil); err != nil {
		t.Fatalf("the baseline must commit: %v", err)
	}
	refused := func(name string, clearHead bool, before func(tx *sql.Tx)) {
		t.Helper()
		if err := f.attempt(t, clearHead, before); !errors.Is(err, finish.ErrCommitRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	refused("a park since the freeze", true, func(tx *sql.Tx) { f.raw(t, tx, "finish.park") })
	refused("a work-admitting row after the close point", true, func(tx *sql.Tx) { f.raw(t, tx, "counter.consume") })
	refused("an approval issue after the close point", true, func(tx *sql.Tx) { f.raw(t, tx, "approval.issue") })
	refused("a typed operation approved after the close point", true, func(tx *sql.Tx) { f.raw(t, tx, "ops.send.third_party.approved") })
	refused("a commit that already exists", true, func(tx *sql.Tx) { f.raw(t, tx, "finish.commit") })
	refused("an operator stop", true, func(tx *sql.Tx) { f.raw(t, tx, "finish.stop") })
	refused("a chain that moved since the read", false, func(tx *sql.Tx) { f.raw(t, tx, "note") })
	// A row that does not admit work does not block the commit.
	if err := f.attempt(t, true, func(tx *sql.Tx) { f.raw(t, tx, "ops.send.third_party.refused:approval") }); err != nil {
		t.Errorf("a refusal row after the close blocked the commit: %v", err)
	}
}

func TestFinishCommitStatementNeedsAFinishingRun(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	walk(t, s, r, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent) // cause "test", not finish
	f := &finFix{s: s, l: s.FinishLedger("p", 1)}
	if err := f.attempt(t, true, nil); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("an ordinary stop committed a finish: %v", err)
	}
	g := newStoreRunning(t)
	if err := g.attempt(t, true, nil); !errors.Is(err, finish.ErrCommitRefused) {
		t.Fatalf("a running run committed a finish: %v", err)
	}
}

func newStoreRunning(t *testing.T) *finFix {
	t.Helper()
	s, _ := newStore(t)
	enter(t, s, "p")
	return &finFix{s: s, l: s.FinishLedger("p", 1)}
}
