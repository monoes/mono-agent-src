package ops

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// StoreAdapter satisfies Charters, Ledger and Auditor over the real store. The store's charter has no run-total
// caps of its own, so they are derived from the limits it does have (see runCaps), and no answer classes, so the
// operator-facing configuration (AnswerClasses) names them; with none, every ask.answer parks.
type StoreAdapter struct {
	Store         store.Store
	Bounds        SpendBounds
	AnswerClasses []string
}

// Active returns the latest generation of the profile if its run is running.
func (a StoreAdapter) Active(ctx context.Context, profile string) (Active, bool, error) {
	next, err := a.Store.NextGeneration(ctx, profile)
	if err != nil {
		return Active{}, false, err
	}
	gen := next - 1
	if gen < 1 {
		return Active{}, false, nil
	}
	run, err := a.Store.GetRun(ctx, profile, gen)
	if err != nil {
		return Active{}, false, err // an unverifiable run is an error, never "no charter that may be skipped"
	}
	if run.State != store.StateRunning {
		return Active{}, false, nil
	}
	c, err := a.Store.LoadCharter(ctx, profile, gen)
	if err != nil {
		return Active{}, false, err
	}
	h := c.Hash()
	return Active{ID: run.CharterID, Hash: hex.EncodeToString(h[:]), Generation: gen, Profile: profile, Constraints: a.constraints(c)}, true, nil
}

// runCaps derives the cumulative caps: the run's budget for spend (an explicit "no ceiling" is the operator's own
// word) and max_total for tasks.
func runCaps(c store.Charter) RunCaps {
	spend := c.Limits.BudgetMicro
	if c.Limits.NoCeiling {
		spend = math.MaxInt64 / 4
	}
	return RunCaps{SpendMicro: spend, Tasks: int64(c.Limits.MaxTotal)}
}

func (a StoreAdapter) constraints(c store.Charter) Constraints {
	out := Constraints{Run: runCaps(c)}
	if c.PerAsk.Declared {
		lim := AskLimits{SpendMicro: c.PerAsk.SpendMicro, Tasks: c.PerAsk.Tasks, Access: c.PerAsk.Access, Depth: c.PerAsk.Depth}
		out.PlanApprove = &PlanApprove{lim}
		if len(a.AnswerClasses) > 0 { // no class lifted: ask.answer is not enabled and every critical ask stays a person item
			out.AskAnswer = &AskAnswer{AskLimits: lim, Classes: slices.Clone(a.AnswerClasses)}
		}
	}
	if slices.Contains(c.Lines, store.LineSpending) {
		out.SpendGrant = &SpendGrant{Targets: []string{"lead", "child"}, MaxPerGrantMicro: a.Bounds.MaxMicro, RunCeilingMicro: max(runCaps(c).SpendMicro, a.Bounds.MaxMicro)}
	}
	return out
}

// Consume charges each counter through the store's conditional insert, one approval id per kind. The store has no
// multi-kind or with-audit transaction, so a failure of a later kind leaves an earlier one charged: over-counting,
// the safe direction. The audit row is appended after the charges.
func (a StoreAdapter) Consume(ctx context.Context, c Consume) (ConsumeResult, error) {
	for _, s := range c.Spends {
		err := a.Store.ConsumeCumulative(ctx, c.Profile, c.Generation, s.Kind, c.ApprovalID+"/"+s.Kind, s.N, s.Cap)
		switch {
		case err == nil:
		case errors.Is(err, store.ErrAlreadyConsumed):
			return ConsumeResult{Replay: true}, nil
		case errors.Is(err, store.ErrOverLimit):
			return ConsumeResult{Over: []string{s.Kind}}, nil
		default:
			return ConsumeResult{}, err
		}
	}
	if err := (a).Append(ctx, c.Audit); err != nil {
		return ConsumeResult{}, err
	}
	return ConsumeResult{Done: true}, nil
}

// Append writes the decision to the audit stream.
func (a StoreAdapter) Append(ctx context.Context, r AuditRow) error {
	p, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = a.Store.AppendAudit(ctx, r.Profile, r.Generation, "ops."+r.Op+"."+r.Result, string(p))
	return err
}
