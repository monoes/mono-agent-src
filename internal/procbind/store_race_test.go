package procbind

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

// barrier is cyclic: n callers arrive, then all go on, round after round.
type barrier struct {
	mu      sync.Mutex
	cond    *sync.Cond
	n, here int
	round   int
}

func newBarrier(n int) *barrier {
	b := &barrier{n: n}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *barrier) wait() {
	b.mu.Lock()
	defer b.mu.Unlock()
	round := b.round
	b.here++
	if b.here == b.n {
		b.here, b.round = 0, b.round+1
		b.cond.Broadcast()
		return
	}
	for round == b.round {
		b.cond.Wait()
	}
}

// barrierTx makes both callers reach each of their statements before either runs it, so a check that is
// separate from the insert would let both pass.
type barrierTx struct {
	DBTX
	b *barrier
}

func (b barrierTx) wait() { b.b.wait() }

func (b barrierTx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	b.wait()
	return b.DBTX.ExecContext(ctx, q, args...)
}

func (b barrierTx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	b.wait()
	return b.DBTX.QueryRowContext(ctx, q, args...)
}

// The cross-family uniqueness holds when two inserts use the database directly (no surrounding transaction).
func TestTheSameIdRacingAcrossFamiliesIsHeldByOneFamilyOnly(t *testing.T) {
	for i := 0; i < 20; i++ {
		db := newDB(t)
		ctx := context.Background()
		x := barrierTx{db, newBarrier(2)}
		x1, x2 := x, x
		errs := make([]error, 2)
		var done sync.WaitGroup
		done.Add(2)
		go func() {
			defer done.Done()
			errs[0] = InsertMissionToken(ctx, x1, storeKey, mission("race", "p1", "owner"))
		}()
		go func() {
			defer done.Done()
			errs[1] = InsertSessionToken(ctx, x2, storeKey, session("race", "p1", "conv", "turn"))
		}()
		done.Wait()
		var m, s int
		if err := db.QueryRow(`SELECT COUNT(*) FROM agent_tokens_v2 WHERE id = 'race'`).Scan(&m); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens_v2 WHERE id = 'race'`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		if m+s != 1 {
			t.Fatalf("round %d: the id is held by %d mission and %d session token(s); errors %v %v", i, m, s, errs[0], errs[1])
		}
	}
}

// A surviving revocation row (its token row deleted after retention) keeps its id from being reused: otherwise a
// revoke of the new token would find the old tombstone and skip it, leaving the new token unrevoked.
func TestAnIdWithASurvivingRevocationRowCannotBeReused(t *testing.T) {
	for _, kind := range []Kind{KindMission, KindSession} {
		db := newDB(t)
		ctx := context.Background()
		old := time.Now().UTC().AddDate(0, 0, -30)
		ins := func(id string) error {
			if kind == KindMission {
				m := mission(id, "p1", "owner")
				m.CreatedAt, m.ExpiresAt = old, old.Add(time.Hour)
				return InsertMissionToken(ctx, db, storeKey, m)
			}
			s := session(id, "p1", "conv", "turn")
			s.CreatedAt, s.ExpiresAt = old, old.Add(time.Hour)
			return InsertSessionToken(ctx, db, storeKey, s)
		}
		table := "agent_tokens_v2"
		if kind == KindSession {
			table = "coder_session_tokens_v2"
		}
		if err := ins("reused"); err != nil {
			t.Fatal(err)
		}
		if _, err := RevokeTx(ctx, db, keyFor(nil), Selector{Profile: "p1", TokenID: "reused"}, "x", old); err != nil {
			t.Fatal(err)
		}
		if err := execErr(db, "DELETE FROM "+table+" WHERE id = 'reused'"); err != nil {
			t.Fatalf("%s: the long-expired token row could not be deleted: %v", kind, err)
		}
		if err := ins("reused"); !errors.Is(err, ErrIDInUse) {
			t.Errorf("%s: an id with a surviving revocation row was reused: %v", kind, err)
		}
		// the other family is refused too
		other := func() error {
			if kind == KindMission {
				return InsertSessionToken(ctx, db, storeKey, session("reused", "p1", "conv", "turn"))
			}
			return InsertMissionToken(ctx, db, storeKey, mission("reused", "p1", "owner"))
		}
		if err := other(); !errors.Is(err, ErrIDInUse) {
			t.Errorf("%s: the other family reused the tombstoned id: %v", kind, err)
		}
	}
}
