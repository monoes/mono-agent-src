// Package ops is the typed-operation allowlist of the open-ended ("doomsday") mode and the per-ask limits of the
// boss's auto-approvals (spec 4.1, 6; note 13.4 section 4B). It decides; it holds no state of its own. Everything
// it needs from the store (the live charter, the facts of a plan or ask, the cumulative counters, the audit
// stream, the person items) is an interface defined here, so the store package satisfies them without this
// package importing it.
//
// Default deny: with no live charter, or a charter that does not enable the operation, Decide refuses before it
// reads anything else, and the host behaves exactly as it does without the mode.
package ops

import (
	"errors"
	"fmt"
	"slices"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Operation ids on the allowlist. Anything else is denied (T15).
const (
	OpPlanApprove = "plan.approve"
	OpAskAnswer   = "ask.answer"
	OpSpendGrant  = "spend.grant"
	// OpFinishBegin starts the finish of the run (note 13.6 A3). It is on the allowlist but is not an approval-with-limits
	// operation: it has no approval record and no per-ask limits, and its precondition check is its enforcement function
	// (BeginFinish). Decide does not decide it.
	OpFinishBegin = "finish.begin"
)

// Allowed says whether op is on the allowlist.
func Allowed(op string) bool {
	return op == OpPlanApprove || op == OpAskAnswer || op == OpSpendGrant || op == OpFinishBegin
}

// Counter kinds of the run's cumulative totals (insert-only delta rows in the store).
const (
	KindAskSpend   = "ask_spend_micro"
	KindAskTasks   = "ask_tasks"
	KindGrantSpend = "grant_spend_micro"

	// Counters of the other typed operations (typed.go): sends, files touched and bytes written, vault.use attempts.
	KindSends         = "sends"
	KindLocalCount    = "local_count"
	KindLocalBytes    = "local_bytes"
	KindVaultAttempts = "vault_attempts"
)

// Names of the limits that a request can exceed, as the audit and the person item name them.
const (
	LimitAskSpend      = "ask_spend_micro"
	LimitAskTasks      = "ask_tasks"
	LimitAskAccess     = "ask_access"
	LimitAskDepth      = "ask_depth"
	LimitRunSpend      = "run_spend_micro"
	LimitRunTasks      = "run_tasks"
	LimitNeverLiftable = "never_liftable"
	LimitClass         = "answer_class"
	LimitGrantTarget   = "grant_target"
	LimitGrantMax      = "grant_max_per_grant_micro"
	LimitGrantRun      = "grant_run_ceiling_micro"
)

// MaxAskDepth is the most levels below the asker a charter may declare for a per-ask limit.
const MaxAskDepth = tasks.MaxCharterSessionDepth

// AskLimits are the per-ask limits of the charter (spec 3.1). "Small" means every one is met.
type AskLimits struct {
	SpendMicro int64  `json:"ask_spend_micro"` // the spend the plan or answer may commit, in micro-dollars
	Tasks      int    `json:"ask_tasks"`       // the task count
	Access     string `json:"ask_access"`      // a ceiling on the access level, never "full"
	Depth      int    `json:"ask_depth"`       // levels below the asker
}

// RunCaps are the ceilings of the run's cumulative auto-approved totals, so many small asks cannot add up to what
// one large ask would have been.
type RunCaps struct {
	SpendMicro int64 `json:"run_spend_micro"`
	Tasks      int64 `json:"run_tasks"`
}

// PlanApprove enables plan.approve.
type PlanApprove struct {
	AskLimits
}

// AskAnswer enables ask.answer for the listed answer classes only; a class the charter did not lift is parked.
type AskAnswer struct {
	AskLimits
	Classes []string `json:"classes"`
}

// SpendGrant enables spend.grant: targets that may receive, the most per grant and the run ceiling, all integer
// micro-dollars. The existing carve and ancestor bounds still apply at the grant itself (spec 4.3).
type SpendGrant struct {
	Targets          []string `json:"targets"` // budget request targets ("lead", "child")
	MaxPerGrantMicro int64    `json:"max_per_grant_micro"`
	RunCeilingMicro  int64    `json:"run_ceiling_micro"`
}

// Constraints are what a charter declares for the operations of this package. A nil entry enables nothing.
type Constraints struct {
	PlanApprove *PlanApprove `json:"plan_approve,omitempty"`
	AskAnswer   *AskAnswer   `json:"ask_answer,omitempty"`
	SpendGrant  *SpendGrant  `json:"spend_grant,omitempty"`
	Run         RunCaps      `json:"run"`

	// The other typed operations of note 13.4 (typed.go). The store's charter does not carry them yet, so the
	// adapter leaves them nil and they stay off: a nil entry enables nothing.
	SendThirdParty   *SendThirdParty   `json:"send_third_party,omitempty"`
	VaultUse         *VaultUse         `json:"vault_use,omitempty"`
	LocalDestructive *LocalDestructive `json:"local_destructive,omitempty"`
	HostSetting      *HostSetting      `json:"host_setting,omitempty"`
}

// SpendBounds are the bounds of a single budget grant the host already enforces (budgetreq.go:
// MinBudgetRequestUSD and MaxBudgetRequestUSD), in micro-dollars. The host passes them in; a charter cannot
// declare a per-grant limit above Max.
type SpendBounds struct {
	MinMicro int64
	MaxMicro int64
}

// Errors. A refusal is not a park: a park is a decision (Verdict), a refusal is an error.
var (
	ErrNoCharter       = errors.New("doomsday ops: no live charter")
	ErrCharterStale    = errors.New("doomsday ops: the charter, generation or profile is not the live one")
	ErrNotAllowlisted  = errors.New("doomsday ops: the operation is not on the allowlist")
	ErrNotEnabled      = errors.New("doomsday ops: the charter does not enable this operation")
	ErrCallerMismatch  = errors.New("doomsday ops: the approval is addressed to another session")
	ErrPlanChanged     = errors.New("doomsday ops: the plan changed since the answer")
	ErrNotOpen         = errors.New("doomsday ops: the subject is not open for an answer")
	ErrAlreadyConsumed = errors.New("doomsday ops: that approval is already spent")
	ErrBadArguments    = errors.New("doomsday ops: the arguments are not valid")
	ErrInvalidLimits   = errors.New("doomsday ops: the charter's limits are not valid")
)

func invalidLimit(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidLimits, fmt.Sprintf(format, a...))
}

func (l AskLimits) validate(name string) error {
	switch {
	case l.SpendMicro < 0:
		return invalidLimit("%s: ask_spend_micro is negative", name)
	case l.Tasks < 1:
		return invalidLimit("%s: ask_tasks must be at least 1", name)
	case l.Depth < 1 || l.Depth > MaxAskDepth:
		return invalidLimit("%s: ask_depth is 1 to %d", name, MaxAskDepth)
	case l.Access == "full" || !slices.Contains(tasks.AccessLevels, l.Access):
		return invalidLimit("%s: ask_access %q is not an access level below full", name, l.Access)
	}
	return nil
}

// Validate checks the constraints at charter entry: every field typed, a limit missing is refused, never read as
// unlimited. A charter whose constraints do not validate is refused whole.
func (c Constraints) Validate(b SpendBounds) error {
	if c.PlanApprove != nil || c.AskAnswer != nil {
		if c.Run.SpendMicro < 0 || c.Run.Tasks < 1 {
			return invalidLimit("run caps: run_tasks must be at least 1 and run_spend_micro not negative")
		}
	}
	if p := c.PlanApprove; p != nil {
		if err := p.validate(OpPlanApprove); err != nil {
			return err
		}
		if int64(p.Tasks) > c.Run.Tasks || p.SpendMicro > c.Run.SpendMicro {
			return invalidLimit("%s: a per-ask limit is above the run cap", OpPlanApprove)
		}
	}
	if a := c.AskAnswer; a != nil {
		if err := a.validate(OpAskAnswer); err != nil {
			return err
		}
		if len(a.Classes) == 0 || slices.Contains(a.Classes, "") {
			return invalidLimit("%s: classes must name at least one answer class", OpAskAnswer)
		}
		if int64(a.Tasks) > c.Run.Tasks || a.SpendMicro > c.Run.SpendMicro {
			return invalidLimit("%s: a per-ask limit is above the run cap", OpAskAnswer)
		}
	}
	if g := c.SpendGrant; g != nil {
		switch {
		case len(g.Targets) == 0:
			return invalidLimit("%s: targets must be listed", OpSpendGrant)
		case g.MaxPerGrantMicro < b.MinMicro || g.MaxPerGrantMicro > b.MaxMicro:
			return invalidLimit("%s: max_per_grant_micro is %d to %d", OpSpendGrant, b.MinMicro, b.MaxMicro)
		case g.RunCeilingMicro < g.MaxPerGrantMicro:
			return invalidLimit("%s: run_ceiling_micro is below max_per_grant_micro", OpSpendGrant)
		}
	}
	return c.validateTyped()
}

func (c Constraints) validateTyped() error {
	if v := c.VaultUse; v != nil {
		if err := v.Validate(); err != nil {
			return err
		}
	}
	if s := c.SendThirdParty; s != nil {
		if err := s.validate(); err != nil {
			return err
		}
	}
	if l := c.LocalDestructive; l != nil {
		if err := l.validate(); err != nil {
			return err
		}
	}
	if h := c.HostSetting; h != nil {
		return h.validate()
	}
	return nil
}
