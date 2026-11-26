package dynorg

import (
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Cost estimates (#230): a worker whose runtime reports no cost (codex,
// copilot, …) still counts toward the org's budget, priced from the tokens
// its exec reports with agentroster's built-in table. The estimate is
// marked (costEstimated in usage.updated and agent.finished), so the UI
// shows it as "≈". A run with neither a cost nor tokens, or on a model the
// table can't price, counts nothing, as before: MaxTurns and the timeout
// bound it.
//
// Estimates run high: exec reports no cache split, so all input is priced
// as uncached, and a subscription plan (codex on ChatGPT, copilot) has no
// per-token bill at all. So they count toward a budget, and can stop a
// worker, only when the user set one (coder set --org-budget-usd; there is
// no default budget, so Limits.BudgetUSD > 0 is always the user's). With
// none they are only shown.

// runCost is what one exec costs toward the budget: the cost its runtime
// reported, else an estimate from its tokens. ok is false when neither is
// known.
func runCost(m Model, run *monomind.TurnResult) (cost float64, estimated, ok bool) {
	hasTok := run.HasInputTokens || run.HasOutputTokens
	// monomind reports $0 for runtimes that report no cost (the M1
	// report's bug 3): from one of those, $0 next to tokens used is no
	// report at all.
	if run.HasCostUSD && (m.ReportsCost || run.CostUSD != 0 || !hasTok || run.InputTokens+run.OutputTokens == 0) {
		return run.CostUSD, false, true
	}
	if hasTok {
		if est, priced := agentroster.TokenCost(m.Runtime, m.Model, run.InputTokens, run.OutputTokens); priced {
			return est, true, true
		}
	}
	return run.CostUSD, false, run.HasCostUSD
}

// addRunCostLocked adds one exec's cost to its worker's and the org's.
func (c *Conductor) addRunCostLocked(w *worker, m Model, run *monomind.TurnResult) {
	cost, estimated, ok := runCost(m, run)
	if !ok {
		return
	}
	w.cost += cost
	w.hasCost = true
	c.cost += cost
	if estimated {
		w.costEstimated = true
		c.costEstimated = true
	}
}

// liveCost is a running exec's cost so far. A runtime that reports cost
// reports it at the end, so until then it has none, not an estimate.
func liveCost(m Model, run *monomind.TurnResult) (cost float64, estimated, ok bool) {
	if m.ReportsCost {
		return run.CostUSD, false, run.HasCostUSD
	}
	return runCost(m, run)
}

// overEstimate reports whether a running exec's estimated cost has reached
// the budget it was given. A reported cost is monomind's to enforce
// (--budget-usd); an estimate only the conductor can see.
func overEstimate(m Model, run *monomind.TurnResult, budget float64) bool {
	if budget <= 0 {
		return false
	}
	cost, estimated, _ := liveCost(m, run)
	return estimated && cost >= budget
}

// cutShort reports whether a run did not complete on its own: the budget
// stop's cancel cut it off, or it never finished.
func cutShort(run *monomind.TurnResult) bool {
	return !run.SawDone || run.StopReason == monomind.StopCancelled
}

// finalRun is an exec's accounting: Exec's result, with the events' usage
// for any metric the result lacks (a run cancelled at the budget may
// return none); the events alone when Exec returned no result.
func finalRun(res *monomind.TurnResult, events monomind.TurnResult) monomind.TurnResult {
	if res == nil {
		return events
	}
	run := *res
	if !run.HasInputTokens && events.HasInputTokens {
		run.InputTokens, run.HasInputTokens = events.InputTokens, true
	}
	if !run.HasOutputTokens && events.HasOutputTokens {
		run.OutputTokens, run.HasOutputTokens = events.OutputTokens, true
	}
	if !run.HasCostUSD && events.HasCostUSD {
		run.CostUSD, run.HasCostUSD = events.CostUSD, true
	}
	if run.SessionID == "" {
		run.SessionID = events.SessionID
	}
	return run
}

// spent is the org's worker cost so far.
func (c *Conductor) spent() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cost
}
