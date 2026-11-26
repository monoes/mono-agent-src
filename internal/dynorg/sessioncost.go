package dynorg

import (
	"context"

	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/monomind"
)

// claude reports a resumed session's cost as the total for the whole session (#1744), so a worker run that resumes
// its session (a follow-up, a veteran, a compaction) costs what it added to the total the worker's last run ended at.
// The rule is the one the conversation's spend applies to lead turns (coderspend.Added): it subtracts only where the
// same session evidently continues, and every doubt is charged in full.

// sessionCost is a runtime session and the total cost it had reached. The zero value subtracts nothing.
type sessionCost struct {
	session string
	usd     float64
}

// own is run with the cost it added itself. It is whole unless run reports a cost for the very session of b.
func (b sessionCost) own(run monomind.TurnResult) monomind.TurnResult {
	if b.session != "" && run.HasCostUSD && run.SessionID == b.session {
		run.CostUSD = coderspend.Added(b.usd, run.CostUSD)
	}
	return run
}

// resumedFromLocked is what a run of w on m that resumes the session resume starts from. Callers hold c.mu.
func (w *worker) resumedFromLocked(m Model, resume string) sessionCost {
	if m.Runtime != coderspend.CumulativeRuntime || resume == "" || w.billed.session != resume {
		return sessionCost{}
	}
	return w.billed
}

// billLocked remembers the total w's session reached with the run total, as the runtime reported it. A run that named
// no session or no usable cost leaves the last one, so the next run is charged what that run spent too. Callers hold
// c.mu.
func (w *worker) billLocked(m Model, total monomind.TurnResult) {
	if m.Runtime == coderspend.CumulativeRuntime && total.SessionID != "" && total.HasCostUSD && finiteCost(total.CostUSD) {
		w.billed = sessionCost{session: total.SessionID, usd: total.CostUSD}
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
