// Package policy holds the strategy layer's decision rules that need no I/O of their own: when the run reads as ready
// for a finish, how a planner's proposal is taken in, and how open directions are ranked. It is a plain library: no
// init, no goroutine, nothing registered, nothing wired.
//
// Strategy never starts a finish and never acts as the boss. The only thing this package can do about one is mark, once
// per judged epoch, that the run READS as ready (Decide); only the verified boss session calls finish.begin, through the
// ops package's boss check, and the finish package (verifier, rubric, review gates) still decides.
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
	// ActionReady: the run reads as ready for a finish. It is a marker for the boss to read; nothing is asked of anyone.
	ActionReady Action = "ready"
	// ActionWait: not now. Nothing is recorded.
	ActionWait Action = "wait"
	// ActionGiveUp: the bound of parked finish rounds is spent. Stop marking, notify the operator once.
	ActionGiveUp Action = "give-up"
	// ActionPark: a finish was parked and the judgment has not moved since; wait for a new one.
	ActionPark Action = "park"
)

// Reason is the typed cause of a decision, for the audit line and the status verbs.
type Reason string

const (
	ReasonReady             Reason = "all-criteria-plausibly-met"
	ReasonRunNotRunning     Reason = "run-not-running"
	ReasonInProgress        Reason = "finish-in-progress"
	ReasonNoCriteria        Reason = "charter-has-no-criteria"
	ReasonParkedRounds      Reason = "parked-finish-rounds-spent"
	ReasonNoJudgment        Reason = "no-judgment"
	ReasonLatestBad         Reason = "latest-epoch-is-a-bad-judgment"
	ReasonStale             Reason = "judgment-stale"
	ReasonSubjectUnknown    Reason = "subject-time-unknown"
	ReasonNotAllMet         Reason = "not-every-criterion-plausibly-met"
	ReasonUnjudgedDirection Reason = "open-direction-not-in-the-judgment"
	ReasonAwaitingNewJudg   Reason = "finish-parked-awaiting-new-judgment"
)

// Decision is the pure result of Decide.
type Decision struct {
	Action Action
	Reason Reason
}

func (d Decision) String() string { return string(d.Action) + ": " + string(d.Reason) }

// Ready reports whether the decision is the ready marker.
func (d Decision) Ready() bool { return d.Action == ActionReady }

// State is everything Decide looks at. The host fills it from the database. Only Judgment (its per-criterion statuses)
// comes from model text, already decoded and validated by the judge package; everything else is host-derived: the
// criterion ids from the charter, the run and epoch state and times from the tables, the direction ids the host minted,
// the finish engine's state and counts from the audit chain. No direction title, rationale or note enters a decision.
type State struct {
	RunRunning       bool
	CriterionIDs     []string
	OpenDirectionIDs []string        // ids of the directions still being worked
	Judgment         *types.Judgment // the judgment of the LATEST epoch that is judged; nil when there is none
	LatestBad        bool            // the latest closed epoch is a bad judgment: it invalidates every earlier reading
	EpochSeq         int64           // the epoch that judgment belongs to
	JudgmentHash     string          // hex of its body hash
	JudgmentAt       time.Time       // when that epoch began
	SubjectKnown     bool            // the host could tell when the verified subject last changed
	SubjectChangedAt time.Time       // last change of the verified subject / freeze; zero when never
	LastParkedAt     time.Time       // when the last finish was parked; zero when never
	ParkedRounds     int             // types.MaxParkedFinishRounds is the bound
	FinishInProgress bool
	GaveUpNotified   bool // the operator was already told; the Runner does not notify twice
	ReadyRecorded    bool // the ready marker of this epoch and judgment is already in the audit chain
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

// Decide is a pure, deterministic function of its arguments. Ready is true only when the LATEST judged epoch is the
// fresh all-met one: a stale judgment, a later bad judgment, a later reading that is not all met, or a direction the
// judgment never saw all clear it.
func (c Config) Decide(s State) Decision {
	switch {
	case !s.RunRunning:
		return Decision{ActionWait, ReasonRunNotRunning}
	case s.FinishInProgress:
		return Decision{ActionWait, ReasonInProgress}
	case len(s.CriterionIDs) == 0:
		return Decision{ActionWait, ReasonNoCriteria}
	case s.ParkedRounds >= types.MaxParkedFinishRounds:
		return Decision{ActionGiveUp, ReasonParkedRounds}
	case s.LatestBad:
		return Decision{ActionWait, ReasonLatestBad}
	case s.Judgment == nil:
		return Decision{ActionWait, ReasonNoJudgment}
	case !s.SubjectKnown:
		return Decision{ActionWait, ReasonSubjectUnknown}
	case c.stale(s):
		return Decision{ActionWait, ReasonStale}
	case !s.Judgment.AllPlausiblyMet(s.CriterionIDs):
		return Decision{ActionWait, ReasonNotAllMet}
	case !coversDirections(s.Judgment, s.OpenDirectionIDs):
		return Decision{ActionWait, ReasonUnjudgedDirection}
	case !s.LastParkedAt.IsZero() && !s.JudgmentAt.After(s.LastParkedAt):
		return Decision{ActionPark, ReasonAwaitingNewJudg}
	}
	return Decision{ActionReady, ReasonReady}
}

// ReadyDetail is the payload of the ready marker: the reason, the epoch and the judgment hash it is bound to. The host
// looks a marker up by this exact text, so a marker is written once per (epoch, judgment).
func ReadyDetail(r Reason, epochSeq int64, judgment string) string {
	return fmt.Sprintf("%s epoch=%d judgment=%s", r, epochSeq, judgment)
}

// coversDirections: every direction still open was seen by the judgment (a direction proposed after it is unjudged).
func coversDirections(j *types.Judgment, open []string) bool {
	seen := make(map[string]bool, len(j.Directions))
	for _, d := range j.Directions {
		seen[d.ID] = true
	}
	for _, id := range open {
		if !seen[id] {
			return false
		}
	}
	return true
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

// Sink is the audit / notification seam. Record appends a strategy.* audit line; the host writes it through the audit
// chain. A give-up is recorded here and the operator is told through the same seam; it never creates a person item.
type Sink interface {
	Record(ctx context.Context, kind, detail string) error
}

// ErrAudit means a line could not be recorded.
var ErrAudit = errors.New("policy: audit line could not be recorded")

// Runner applies Decide and records the markers. It holds no finish engine and calls nobody: the ready marker is a
// witnessed audit row, nothing else. It re-decides from the state itself, so a caller cannot hand it a decision.
type Runner struct {
	Config Config
	Sink   Sink
}

// Step decides and records. A ready marker is written at most once per (epoch, judgment): the caller says whether it
// already exists (ReadyRecorded), and a failed write is not retried by this function.
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
		return d, r.record(ctx, types.KindFinishGaveUp, fmt.Sprintf("%s rounds=%d", d.Reason, s.ParkedRounds))
	case ActionReady:
		if s.ReadyRecorded {
			return d, nil
		}
		return d, r.record(ctx, types.KindFinishReady, ReadyDetail(d.Reason, s.EpochSeq, s.JudgmentHash))
	}
	return d, nil
}

func (r Runner) record(ctx context.Context, kind, detail string) error {
	if r.Sink == nil {
		return fmt.Errorf("%w: no sink", ErrAudit)
	}
	if err := r.Sink.Record(ctx, kind, detail); err != nil {
		return fmt.Errorf("%w: %v", ErrAudit, err)
	}
	return nil
}
