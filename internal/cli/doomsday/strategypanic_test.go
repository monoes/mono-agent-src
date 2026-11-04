//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Panic after the audit write inside the policy sink's transaction (ready marker): is the connection/lock left clean?
type panicAppender struct {
	inner ChainTxAppender
	kind  string
	armed atomic.Bool
}

func (p *panicAppender) AppendAuditTx(ctx context.Context, tx sstore.Querier, profile string, gen int64, kind, payload string) error {
	err := p.inner.AppendAuditTx(ctx, tx, profile, gen, kind, payload)
	if kind == p.kind && p.armed.CompareAndSwap(true, false) {
		panic("boom")
	}
	return err
}

func writeProbe(db *sql.DB, d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS strategy_panic_probe (x INTEGER)`)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO strategy_panic_probe VALUES (1)`)
	return err
}

func TestRecoveredPanicInAuditTxLeavesTheWriteLockFree(t *testing.T) {
	for _, kind := range []string{types.KindFinishReady, types.KindEpochBegin} {
		t.Run(kind, func(t *testing.T) {
			var pa *panicAppender
			r := newStratRig(t, finishCharterJSON, func(d *StrategyHostDeps) {
				pa = &panicAppender{inner: d.Audit, kind: kind}
				d.Audit = pa
			})
			r.judge.set(types.CritPlausiblyMet)
			r.propose(t, "dir one")
			r.clock.Advance(31 * time.Minute)
			pa.armed.Store(true)
			l := &StrategyLoop{Build: func(context.Context) (*StrategyHost, error) { return r.host, nil }, Logf: func(f string, a ...any) { t.Logf(f, a...) }}
			tk, err := l.Step(bg)
			t.Logf("step after panic: %+v err=%v", tk, err)
			stats := r.w.DB.Stats()
			t.Logf("pool: open=%d inUse=%d idle=%d", stats.OpenConnections, stats.InUse, stats.Idle)
			err = writeProbe(r.w.DB, 7*time.Second)
			t.Logf("RESULT other writer after the recovered panic: err=%v inUse=%d", err, r.w.DB.Stats().InUse)
			if err != nil {
				t.Errorf("another writer is locked out after a recovered panic: %v", err)
			}
			// the loop's next step must work
			r.clock.Advance(31 * time.Minute)
			tk, err = l.Step(bg)
			t.Logf("next step: %+v err=%v", tk, err)
			r.chainAndWitnessAccept(t)
		})
	}
}
