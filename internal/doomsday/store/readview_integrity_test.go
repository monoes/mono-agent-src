package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func cutAuditTail(t *testing.T, db *sql.DB, keepUpTo int) {
	t.Helper()
	if _, err := db.Exec(`DROP TRIGGER doomsday_audit_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM doomsday_audit WHERE profile = 'p' AND seq > ?`, keepUpTo); err != nil {
		t.Fatal(err)
	}
}

// A chain whose tail was cut is still internally consistent; the view must not call a chain without its run.enter
// row, or an empty one, verified.
func TestReadViewEmptyChainIsNeverVerified(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	cutAuditTail(t, db, 0)
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Chain.Verified || v.Chain.Reason != ReasonDoesNotVerify || len(v.Feed) != 0 {
		t.Fatalf("an empty chain verified: %+v", v.Chain)
	}
}

func TestReadViewChainMustStartWithRunEnter(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	appendN(t, s, r, 2)
	if _, err := db.Exec(`DROP TRIGGER doomsday_audit_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM doomsday_audit WHERE profile = 'p' AND seq = 1`); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{})
	if err != nil || v.Chain.Verified {
		t.Fatalf("a chain without its first row verified: %+v %v", v.Chain, err)
	}
}

// The store alone cannot see a cut tail: Verified says consistent and Anchored stays false, so no layer may call it
// anchored on the store's word.
func TestReadViewCutTailIsNeverAnchoredByTheStore(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	appendN(t, s, r, 3)
	cutAuditTail(t, db, 2)
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Chain.Anchored || v.Chain.HeadSeq != 2 {
		t.Fatalf("store claimed an anchor for a cut chain: %+v", v.Chain)
	}
}

func TestReadViewDeletedNewestRunRowIsNotAnOlderRunOrNoRun(t *testing.T) {
	s, db := newStore(t)
	r1 := enter(t, s, "p")
	walk(t, s, r1, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent)
	enter(t, s, "p") // generation 2
	if _, err := db.Exec(`DROP TRIGGER doomsday_runs_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM doomsday_runs WHERE profile = 'p' AND generation = 2`); err != nil {
		t.Fatal(err)
	}
	for _, gen := range []int64{0, 2} {
		v, err := s.ReadView(ctx, "p", gen, ViewQuery{})
		if err != nil || v.Chain.Verified || v.Chain.Reason != ReasonDoesNotVerify {
			t.Fatalf("gen %d: a deleted run row went unnoticed: %+v %v", gen, v, err)
		}
	}
	// the older generation is still readable on its own
	if v, err := s.ReadView(ctx, "p", 1, ViewQuery{}); err != nil || !v.Chain.Verified {
		t.Fatalf("generation 1: %+v %v", v.Chain, err)
	}
}

func TestReadViewOnlyRunRowDeletedIsNotNoRun(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := db.Exec(`DROP TRIGGER doomsday_runs_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM doomsday_runs`); err != nil {
		t.Fatal(err)
	}
	v, err := s.ReadView(ctx, "p", 0, ViewQuery{})
	if err != nil || v.Chain.Verified {
		t.Fatalf("the only run row deleted: %+v %v", v.Chain, err)
	}
}

func TestReadViewHasATimeBudget(t *testing.T) {
	s, _ := newStore(t)
	r := enter(t, s, "p")
	appendN(t, s, r, 20)
	old := readViewBudget
	// a budget already spent: a deadline in the past. A 1ns budget is not, on a host whose clock ticks coarsely (Windows): the
	// deadline timer fires after the read has finished. The budget itself is a context deadline, enforced the same everywhere.
	readViewBudget = -time.Second
	defer func() { readViewBudget = old }()
	if _, err := s.ReadView(ctx, "p", 0, ViewQuery{}); !errors.Is(err, ErrViewTimeout) {
		t.Fatalf("no timeout: %v", err)
	}
	readViewBudget = old
	if _, err := s.ReadView(ctx, "p", 0, ViewQuery{}); err != nil {
		t.Fatal(err) // and the connection is usable afterwards
	}
}

func TestWithReadOnlyResetsAfterAPanic(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	db.SetMaxOpenConns(1) // the same connection must come back
	done := make(chan any, 1)
	go func() { // a panic that leaves the transaction open blocks conn.Close forever: fail fast instead of hanging
		defer func() { done <- recover() }()
		_ = s.withReadOnly(ctx, func(*sql.Tx) error { panic("boom") })
	}()
	select {
	case p := <-done:
		if p == nil {
			t.Fatal("the panic was swallowed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withReadOnly hung after a panic in fn")
	}
	wrote := make(chan error, 1)
	go func() {
		_, err := s.AppendAudit(ctx, "p", r.Generation, "x.note", "after panic")
		wrote <- err
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatalf("a panic left the pooled connection read-only: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pool is stuck after a panic")
	}
}

// PRAGMA query_only = ON failing (it may have been applied before the error) still resets the connection.
func TestWithReadOnlyResetsWhenQueryOnlyFailsToSwitchOn(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	db.SetMaxOpenConns(1)
	cctx, cancel := context.WithCancel(ctx)
	resets := 0
	testReadOnlySeams.beforeQueryOnly = cancel
	testReadOnlySeams.resetQueryOnly = func(c context.Context, conn *sql.Conn) error {
		resets++
		_, err := conn.ExecContext(c, "PRAGMA query_only = OFF")
		return err
	}
	defer func() { testReadOnlySeams = readOnlySeams{} }()
	if err := s.withReadOnly(cctx, func(*sql.Tx) error { t.Fatal("fn ran"); return nil }); err == nil {
		t.Fatal("the failed PRAGMA was not reported")
	}
	if resets != 1 {
		t.Fatalf("reset ran %d times after a failed PRAGMA", resets)
	}
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "x.note", "after"); err != nil {
		t.Fatal(err)
	}
}

func TestWithReadOnlyResetsWhenBeginTxFails(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	db.SetMaxOpenConns(1)
	cctx, cancel := context.WithCancel(ctx)
	testReadOnlySeams.afterQueryOnly = cancel // BeginTx then sees a cancelled context
	defer func() { testReadOnlySeams = readOnlySeams{} }()
	err := s.withReadOnly(cctx, func(*sql.Tx) error { t.Fatal("fn ran"); return nil })
	if err == nil {
		t.Fatal("BeginTx did not fail")
	}
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "x.note", "after"); err != nil {
		t.Fatalf("a failed BeginTx left the connection read-only: %v", err)
	}
}

func TestWithReadOnlyDiscardsAConnectionItCannotReset(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	db.SetMaxOpenConns(1)
	testReadOnlySeams.resetQueryOnly = func(context.Context, *sql.Conn) error { return errors.New("reset failed") }
	if err := s.withReadOnly(ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	testReadOnlySeams = readOnlySeams{}
	// the read-only connection was dropped, so the pool opens a fresh writable one
	if _, err := s.AppendAudit(ctx, "p", r.Generation, "x.note", "after"); err != nil {
		if strings.Contains(err.Error(), "readonly") || strings.Contains(err.Error(), "read-only") {
			t.Fatalf("a connection that could not be reset went back to the pool: %v", err)
		}
		t.Fatal(err)
	}
}
