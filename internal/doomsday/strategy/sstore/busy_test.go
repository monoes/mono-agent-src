package sstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// flakyDB makes the first `busy` commits fail the way SQLite refuses a deferred transaction whose snapshot went stale
// (SQLITE_BUSY_SNAPSHOT, extended code 517: busy_timeout does not wait it out). The real transaction is rolled back, as
// SQLite does.
type flakyDB struct {
	DB
	busy  int
	begun int
}

type flakyTx struct {
	Tx
	f *flakyDB
}

func (d *flakyDB) Begin(ctx context.Context) (Tx, error) {
	tx, err := d.DB.Begin(ctx)
	d.begun++
	return flakyTx{Tx: tx, f: d}, err
}

func (t flakyTx) Commit() error {
	if t.f.busy > 0 {
		t.f.busy--
		_ = t.Tx.Rollback()
		return errors.New("database is locked (517)")
	}
	return t.Tx.Commit()
}

func withFastBackoff(t *testing.T) {
	t.Helper()
	old := busyBackoff
	busyBackoff = 0
	t.Cleanup(func() { busyBackoff = old })
}

func TestABusySnapshotRunsTheWholeTransactionAgainOnce(t *testing.T) {
	withFastBackoff(t)
	e := newEnv(t)
	f := &flakyDB{DB: WrapDB(e.db), busy: 2}
	s := New(f, e.s.keys, e.sink)
	got, created, err := s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer))
	if err != nil || !created || got.Seq != 1 {
		t.Fatalf("%+v created=%v err=%v", got, created, err)
	}
	if f.begun != 3 {
		t.Fatalf("transactions begun: %d, want 3 (two busy, one good)", f.begun)
	}
	if e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) != 1 || e.count(t, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = ?`, types.KindEpochBegin) != 1 {
		t.Fatal("a retried write left more or less than one epoch and one audit line")
	}
	e.auditOK(t)
}

func TestARetriedJudgmentDoesNotLoseOrDoubleTheAnswer(t *testing.T) {
	withFastBackoff(t)
	e := newEnv(t)
	if _, _, err := e.s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer)); err != nil {
		t.Fatal(err)
	}
	f := &flakyDB{DB: WrapDB(e.db), busy: 1}
	s := New(f, e.s.keys, e.sink)
	created, err := s.RecordJudgment(ctx, e.prof, e.gen, 1, []byte(`{"criteria":[],"directions":[],"next_actions":[]}`))
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_judgments`) != 1 {
		t.Fatal("the judgment is not stored exactly once")
	}
}

func TestBusyRetriesAreBounded(t *testing.T) {
	withFastBackoff(t)
	e := newEnv(t)
	f := &flakyDB{DB: WrapDB(e.db), busy: 1000}
	s := New(f, e.s.keys, e.sink)
	if _, _, err := s.InsertEpoch(ctx, e.epoch(1, types.TriggerTimer)); err == nil || !isBusy(err) {
		t.Fatalf("want the busy error after the bound, got %v", err)
	}
	if f.begun != MaxBusyRetries+1 {
		t.Fatalf("transactions begun: %d, want %d", f.begun, MaxBusyRetries+1)
	}
	if e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) != 0 {
		t.Fatal("a refused write left a row")
	}
}

func TestOnlyBusyErrorsAreRetried(t *testing.T) {
	withFastBackoff(t)
	e := newEnv(t)
	calls := 0
	err := e.s.inTx(ctx, e.gen, 1, func(Tx) error { calls++; return ErrConflict })
	if !errors.Is(err, ErrConflict) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// A panic inside a write must not leave the transaction open (it would hold the write lock until the process ends): the
// deferred rollback frees it before the panic travels on to the loop's recover. With one connection, a leak would block
// the next write forever, so the follow-up runs under a deadline.
func TestAPanicInAWriteRollsTheTransactionBack(t *testing.T) {
	e := newEnv(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		_ = e.s.inTx(ctx, e.gen, 1, func(tx Tx) error {
			if _, err := tx.ExecContext(ctx, `INSERT INTO doomsday_strategy_epochs(profile, generation, epoch_seq, trigger, started_at, state, mac) VALUES (?,?,?,?,?,?,?)`,
				e.prof, e.gen, 1, "timer", 1, "open", []byte("x")); err != nil {
				t.Fatal(err)
			}
			panic("boom")
		})
	}()
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err := e.s.InsertEpoch(dctx, e.epoch(1, types.TriggerTimer)); err != nil {
		t.Fatalf("the write after the panic: %v", err)
	}
	if e.count(t, `SELECT COUNT(*) FROM doomsday_strategy_epochs`) != 1 {
		t.Fatal("the half-written row of the panicking write survived")
	}
}
