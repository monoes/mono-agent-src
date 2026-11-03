// Package policy holds the strategy layer's decision rules that need no I/O of their own: when the root boss may ask
// for a finish, how a planner's proposal is taken in, and how open directions are ranked. It is a plain library: no
// init, no goroutine, nothing registered, nothing wired.
//
// Strategy never decides a finish. The only thing this package can do about one is ask the existing finish engine to
// begin it, and only under the conditions in Decide; the finish package (verifier, rubric, review gates) still decides.
package policy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Action is what the policy says to do about a finish right now.
type Action string

const (
	// ActionRequest: ask the finish engine to begin. It is a request; the engine may still refuse.
	ActionRequest Action = "request"
	// ActionWait: not now. Nothing is recorded and nothing is asked.
	ActionWait Action = "wait"
	// ActionGiveUp: the bound of parked finish rounds is spent. Stop requesting, notify the operator once.
	ActionGiveUp Action = "give-up"
	// ActionPark: a finish was parked and the judgment has not moved since; wait for a new one.
	ActionPark Action = "park"
)

// Reason is the typed cause of a decision, for the audit line.
type Reason string

const (
	ReasonRequested       Reason = "all-criteria-plausibly-met"
	ReasonRunNotRunning   Reason = "run-not-running"
	ReasonInProgress      Reason = "finish-in-progress"
	ReasonNotBoss         Reason = "caller-is-not-the-root-boss"
	ReasonNoCriteria      Reason = "charter-has-no-criteria"
	ReasonParkedRounds    Reason = "parked-finish-rounds-spent"
	ReasonNoJudgment      Reason = "no-judgment"
	ReasonStale           Reason = "judgment-stale"
	ReasonNotAllMet       Reason = "not-every-criterion-plausibly-met"
	ReasonAwaitingNewJudg Reason = "finish-parked-awaiting-new-judgment"
)

// Decision is the pure result of Decide.
type Decision struct {
	Action Action
	Reason Reason
}

func (d Decision) String() string { return string(d.Action) + ": " + string(d.Reason) }

// State is everything Decide looks at. The host fills it from the database; nothing in it comes from model text except
// the Judgment, which the judge package has already decoded and validated.
type State struct {
	RunRunning       bool
	Caller           string // the session asking
	Boss             string // the root boss session of the run; "" never matches anyone
	CriterionIDs     []string
	Judgment         *types.Judgment // the latest NON-bad judgment, nil when there is none
	JudgmentAt       time.Time       // when that judgment was recorded
	SubjectChangedAt time.Time       // last change of the verified subject / freeze; zero when never
	LastParkedAt     time.Time       // when the last finish was parked; zero when never
	ParkedRounds     int             // types.MaxParkedFinishRounds is the bound
	FinishInProgress bool
	GaveUpNotified   bool // the operator was already told; the Runner does not notify twice
	Now              time.Time
}

// Config holds the one tunable of the policy.
type Config struct {
	// MaxJudgmentAge: a judgment older than this is stale. Zero means two default epoch intervals.
	MaxJudgmentAge time.Duration
}

func (c Config) maxAge() time.Duration {
	if c.MaxJudgmentAge <= 0 {
		return 2 * types.DefaultEpochInterval
	}
	return c.MaxJudgmentAge
}

// Decide is a pure, deterministic function of its arguments. The order of the checks is the policy: the cheap
// authority checks come first, then the bound on parked rounds, then the judgment.
func (c Config) Decide(s State) Decision {
	switch {
	case !s.RunRunning:
		return Decision{ActionWait, ReasonRunNotRunning}
	case s.FinishInProgress:
		return Decision{ActionWait, ReasonInProgress}
	case s.Boss == "" || s.Caller != s.Boss:
		return Decision{ActionWait, ReasonNotBoss}
	case len(s.CriterionIDs) == 0:
		return Decision{ActionWait, ReasonNoCriteria}
	case s.ParkedRounds >= types.MaxParkedFinishRounds:
		return Decision{ActionGiveUp, ReasonParkedRounds}
	case s.Judgment == nil:
		return Decision{ActionWait, ReasonNoJudgment}
	case c.stale(s):
		return Decision{ActionWait, ReasonStale}
	case !s.Judgment.AllPlausiblyMet(s.CriterionIDs):
		return Decision{ActionWait, ReasonNotAllMet}
	case !s.LastParkedAt.IsZero() && !s.JudgmentAt.After(s.LastParkedAt):
		return Decision{ActionPark, ReasonAwaitingNewJudg}
	}
	return Decision{ActionRequest, ReasonRequested}
}

// stale fails closed: an unknown time, a time in the future, an old judgment, or one not newer than the last change of
// the verified subject is stale.
func (c Config) stale(s State) bool {
	if s.JudgmentAt.IsZero() || s.Now.IsZero() || s.JudgmentAt.After(s.Now) {
		return true
	}
	if s.Now.Sub(s.JudgmentAt) > c.maxAge() {
		return true
	}
	return !s.SubjectChangedAt.IsZero() && !s.JudgmentAt.After(s.SubjectChangedAt)
}

// Outcome is the finish engine's answer to a begin request. Refused is the engine's reason when it said no.
type Outcome struct {
	Accepted bool
	Refused  string
}

// Finish is the narrow seam to the existing finish engine. The cli package's FinishEngine.FinishBegin returns its own
// report type, so the host adapts it with FinishFunc in one line; this package imports no cli package.
type Finish interface {
	Begin(ctx context.Context) (Outcome, error)
}

// FinishFunc adapts a function to Finish.
type FinishFunc func(ctx context.Context) (Outcome, error)

// Begin calls f.
func (f FinishFunc) Begin(ctx context.Context) (Outcome, error) { return f(ctx) }

// Sink is the audit / notification seam. Record appends a strategy.* audit line; the host writes it through the audit
// chain. A give-up is recorded here and the operator is told through the same seam; it never creates a person item.
type Sink interface {
	Record(ctx context.Context, kind, detail string) error
}

// ErrAudit means a line could not be recorded; nothing was asked of the finish engine.
var ErrAudit = errors.New("policy: audit line could not be recorded")

// Runner applies Decide and, only for ActionRequest, asks the finish engine. It re-decides from the state itself, so a
// caller cannot hand it a decision.
type Runner struct {
	Config Config
	Finish Finish
	Sink   Sink
}

// Step decides and acts. A request is recorded BEFORE it is made (no audit line, no request). The engine's refusal is
// recorded as a parked finish; the engine, not this package, decided it.
func (r Runner) Step(ctx context.Context, s State) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{ActionWait, ReasonRunNotRunning}, err
	}
	d := r.Config.Decide(s)
	switch d.Action {
	case ActionGiveUp:
		if s.GaveUpNotified {
			return d, nil
		}
		return d, r.record(ctx, types.KindFinishGaveUp, d, fmt.Sprintf("rounds=%d", s.ParkedRounds))
	case ActionRequest:
		if r.Finish == nil {
			return Decision{ActionWait, ReasonRunNotRunning}, errors.New("policy: no finish engine")
		}
		if err := r.record(ctx, types.KindFinishRequested, d, ""); err != nil {
			return d, err
		}
		out, err := r.Finish.Begin(ctx)
		if err != nil {
			return d, fmt.Errorf("policy: finish begin: %w", err)
		}
		if !out.Accepted {
			return d, r.record(ctx, types.KindFinishParked, d, "engine refused: "+clip(out.Refused))
		}
	}
	return d, nil
}

func (r Runner) record(ctx context.Context, kind string, d Decision, extra string) error {
	if r.Sink == nil {
		return fmt.Errorf("%w: no sink", ErrAudit)
	}
	detail := string(d.Reason)
	if extra != "" {
		detail += " " + extra
	}
	if err := r.Sink.Record(ctx, kind, detail); err != nil {
		return fmt.Errorf("%w: %v", ErrAudit, err)
	}
	return nil
}

func clip(s string) string {
	if len(s) > types.MaxNoteLen {
		return s[:types.MaxNoteLen]
	}
	return s
}
