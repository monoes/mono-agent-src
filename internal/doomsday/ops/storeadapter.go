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

// StoreAdapter satisfies Charters, Ledger and Auditor over the real store. The charter carries the per-ask limits, the
// run caps, the answer classes and the constraints of the typed operations (store.Typed); an operation whose section is
// absent stays off. A typed operation is consumed only against its issued approval row (ConsumeApproval).
type StoreAdapter struct {
	Store  store.Store
	Bounds SpendBounds
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

// grantCeiling is the run's spend ceiling for grants: the budget, or the host's per-grant maximum if that is larger
// or the charter said "no ceiling" (the budget request bounds still hold at each grant).
func (a StoreAdapter) grantCeiling(c store.Charter) int64 {
	if c.Limits.NoCeiling {
		return math.MaxInt64 / 4
	}
	return max(c.Limits.BudgetMicro, a.Bounds.MaxMicro)
}

func (a StoreAdapter) constraints(c store.Charter) Constraints {
	out := Constraints{}
	if c.PerAsk.Declared {
		lim := AskLimits{SpendMicro: c.PerAsk.SpendMicro, Tasks: c.PerAsk.Tasks, Access: c.PerAsk.Access, Depth: c.PerAsk.Depth}
		out.PlanApprove = &PlanApprove{lim}
		out.Run = RunCaps{SpendMicro: c.PerAsk.RunSpendMicro, Tasks: c.PerAsk.RunTasks}
		if len(c.PerAsk.AnswerClasses) > 0 { // no class lifted: ask.answer is not enabled and every critical ask stays a person item
			out.AskAnswer = &AskAnswer{AskLimits: lim, Classes: slices.Clone(c.PerAsk.AnswerClasses)}
		}
	}
	if slices.Contains(c.Lines, store.LineSpending) {
		targets := []string{"lead", "child"}
		if len(c.Typed.GrantTargets) > 0 {
			targets = slices.Clone(c.Typed.GrantTargets)
		}
		out.SpendGrant = &SpendGrant{Targets: targets, MaxPerGrantMicro: a.Bounds.MaxMicro, RunCeilingMicro: a.grantCeiling(c)}
	}
	return typedConstraints(out, c.Typed)
}

// approvalsKind counts approvals themselves: it is charged one unit per approval, so an approval with only
// zero-sized charges (an ask answer commits nothing) is still recorded as spent and a repeat is a replay.
const approvalsKind = "approvals"

// Consume charges every counter and writes the audit row in one store transaction (all or nothing).
func (a StoreAdapter) Consume(ctx context.Context, c Consume) (ConsumeResult, error) {
	var charges []store.Charge
	for _, s := range c.Spends {
		if s.N > 0 {
			charges = append(charges, store.Charge{Kind: s.Kind, N: s.N, Limit: s.Cap})
		}
	}
	p, err := json.Marshal(withResult(c.Audit, "approved", nil))
	if err != nil {
		return ConsumeResult{}, err
	}
	entry := store.AuditEntry{Kind: "ops." + c.Audit.Op + ".approved", Payload: string(p)}
	if AllowedTyped(c.Audit.Op) {
		err = a.consumeTyped(ctx, c, charges, entry)
	} else {
		mc, ok := a.Store.(store.MultiConsumer)
		if !ok {
			return ConsumeResult{}, errors.New("doomsday ops: the store has no atomic multi-kind consume")
		}
		err = mc.ConsumeMany(ctx, c.Profile, c.Generation, c.ApprovalID, append([]store.Charge{{Kind: approvalsKind, N: 1, Limit: math.MaxInt64}}, charges...), entry)
	}
	var over *store.OverLimitError
	switch {
	case err == nil:
		return ConsumeResult{Done: true}, nil
	case errors.Is(err, store.ErrAlreadyConsumed):
		return ConsumeResult{Replay: true}, nil
	case errors.As(err, &over):
		return ConsumeResult{Over: over.Kinds}, nil
	default:
		return ConsumeResult{}, err // a refusal (run stopped) or any other failure is never an approval
	}
}

// consumeTyped spends a typed approval: there is no fallback to a store that cannot check the approval row.
func (a StoreAdapter) consumeTyped(ctx context.Context, c Consume, charges []store.Charge, entry store.AuditEntry) error {
	as, ok := a.Store.(store.ApprovalStore)
	if !ok {
		return errors.New("doomsday ops: the store has no approval rows")
	}
	err := as.ConsumeApproval(ctx, c.Profile, c.Generation, c.ApprovalID, c.Audit.Op, c.Audit.ArgsHash, charges, entry)
	switch {
	case errors.Is(err, store.ErrApprovalUnknown):
		return ErrUnknownApproval
	case errors.Is(err, store.ErrApprovalRevoked):
		return ErrRevoked
	case errors.Is(err, store.ErrApprovalExpired):
		return ErrExpired
	case errors.Is(err, store.ErrApprovalMismatch):
		return ErrArgsMismatch
	}
	return err
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
