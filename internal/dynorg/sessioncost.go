package dynorg

import (
	"context"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/monomind"
)

// claude reports a resumed session's cost as the total for the whole session when it restored that total (#1744), so
// a worker run that resumes its session (a follow-up, a veteran, a compaction) costs what it added to the totals the
// worker's last run ended at. The rule is the one the conversation's spend applies to lead turns (coderspend.Added):
// it subtracts only when the cost and every token counter held, and every doubt is charged in full.

// sessionCost is a runtime session and the totals it had reached. The zero value subtracts nothing.
type sessionCost struct {
	session string
	totals  coderspend.Totals
}

func totalsOf(run monomind.TurnResult) coderspend.Totals {
	return coderspend.Totals{CostUSD: run.CostUSD, Models: chatevents.ModelUsageOf(run.ModelUsage)}
}

// own is run with the cost it added itself. It is whole unless run reports a cost for the very session of b.
func (b sessionCost) own(run monomind.TurnResult) monomind.TurnResult {
	if b.session != "" && run.HasCostUSD && run.SessionID == b.session {
		run.CostUSD = coderspend.Added(b.totals, totalsOf(run))
	}
	return run
}

// pending says run's own cost is not known yet: it resumes b's session, b has counters to compare with, and run's
// arrive with its result, after the usage event that already carries the cost. A charge is never lowered, so until
// then the live accounting leaves that cost out; a run that ends without counters is charged whole (own).
func (b sessionCost) pending(run monomind.TurnResult) bool {
	return b.session != "" && run.HasCostUSD && run.SessionID == b.session && run.ModelUsage == nil && b.totals.Comparable()
}

// resumedFromLocked is what a run of w on m that resumes the session resume starts from. Callers hold c.mu.
func (w *worker) resumedFromLocked(m Model, resume string) sessionCost {
	if m.Runtime != coderspend.CumulativeRuntime || resume == "" || w.billed.session != resume {
		return sessionCost{}
	}
	return w.billed
}

// billLocked remembers the totals w's session reached with the run total, as the runtime reported it. A run that named
// no session or no usable cost leaves the last one, so the next run is charged what that run spent too. Callers hold
// c.mu.
func (w *worker) billLocked(m Model, total monomind.TurnResult) {
	if m.Runtime == coderspend.CumulativeRuntime && total.SessionID != "" && total.HasCostUSD && finiteCost(total.CostUSD) {
		w.billed = sessionCost{session: total.SessionID, totals: totalsOf(total)}
	}
}

type sessionCostKey struct{}

func withSessionCost(ctx context.Context, b sessionCost) context.Context {
	return context.WithValue(ctx, sessionCostKey{}, b)
}

// RunCostUSD is what res, the result of the worker run whose Config.Exec was called with ctx, cost by itself: for an
// Exec wrapper that does its own accounting (an AI task's workers' pool).
func RunCostUSD(ctx context.Context, res monomind.TurnResult) float64 {
	b, _ := ctx.Value(sessionCostKey{}).(sessionCost)
	return b.own(res).CostUSD
}
