package verifier

import (
	"context"
	"time"
)

// Caps of one check (host-enforced here, not asked of the criteria).
const (
	MaxCriteria      = 16
	CriterionTimeout = 2 * time.Minute // one criterion
	TotalTimeout     = 20 * time.Minute
	OutputCap        = 1 << 20 // bytes of stdout and of stderr of one criterion
	ExcerptCap       = 1 << 10 // bytes of an excerpt kept in a result
	detailCap        = 200
	// DefaultCallCapUSD is the most one reviewer call may cost (the judge's per-call cap, kept equal by a test).
	DefaultCallCapUSD = 0.5
)

// What a result says about where the criteria ran.
const (
	// ContainmentUnconfined: the process group and the finish sandbox's limits and empty network namespace, and nothing else.
	// The filesystem, unix sockets, signals and /proc are NOT confined, and a child that calls setsid escapes the group kill.
	ContainmentUnconfined = "process-group UNCONFINED"
	// ContainmentFileOnly: no program was run, so there was nothing to confine.
	ContainmentFileOnly = "none (no program was run)"
	// ContainmentConfinedPrefix is followed by the confiner's name: "confined: <name> + process-group".
	ContainmentConfinedPrefix = "confined: "
)

// Containment is the unconfined statement, kept under its old name for callers that only want to compare.
const Containment = ContainmentUnconfined

// Outcome is the closed list of what a check concluded.
type Outcome string

const (
	OutcomePassedMachine Outcome = "passed-machine"
	OutcomeFailed        Outcome = "failed"
	OutcomeError         Outcome = "error"
	OutcomeBlocked       Outcome = "blocked"
	OutcomeStale         Outcome = "stale"
	OutcomeCannotCheck   Outcome = "cannot-check"
	OutcomeAbandoned     Outcome = "abandoned"
)

// Reason is the closed list of why. Every outcome other than passed-machine carries one.
type Reason string

const (
	ReasonNone Reason = ""
	// failed, error
	ReasonCriteriaFailed Reason = "criteria-failed"
	ReasonCriterionError Reason = "criterion-error"
	// blocked
	ReasonReviewerBlocked     Reason = "reviewer-blocked"
	ReasonReviewerUnavailable Reason = "reviewer-unavailable"
	ReasonReviewerInvalid     Reason = "reviewer-invalid"
	ReasonReviewerPinChanged  Reason = "reviewer-pin-changed"
	ReasonLeadChanged         Reason = "lead-changed"
	ReasonNoBudget            Reason = "no-budget"
	// stale
	ReasonSubjectChanged Reason = "subject-changed"
	// cannot-check
	ReasonNoNetworkBlock    Reason = "no-network-block"
	ReasonNoConfinement     Reason = "no-confinement"
	ReasonNoContainment     Reason = "no-containment"
	ReasonPinNotIndependent Reason = "pin-not-independent"
	ReasonSnapshotRefused   Reason = "snapshot-refused"
	ReasonTooManyCriteria   Reason = "too-many-criteria"
	ReasonNoMachineCriteria Reason = "no-machine-criteria"
	ReasonBadRequest        Reason = "bad-request"
	// abandoned
	ReasonCancelled   Reason = "cancelled"
	ReasonRunDeadline Reason = "run-deadline"
)

// Message is the plain-words line for a person; for the refusals it is the line the design names.
func Message(o Outcome, r Reason) string {
	switch {
	case o == OutcomeCannotCheck && r == ReasonNoNetworkBlock:
		return "cannot-check: no network block"
	case o == OutcomeCannotCheck && r == ReasonNoConfinement:
		return "this computer can't confine project commands, so they are not run"
	case o == OutcomePassedMachine:
		return string(o)
	case r == ReasonNone:
		return string(o)
	}
	return string(o) + ": " + string(r)
}

// Result of one criterion: the closed list, from the finish harness plus "person" (a rubric criterion, not run) and
// "not-run" (the check ended before it).
const (
	ResPass     = "pass"
	ResFail     = "fail"
	ResError    = "error"
	ResTimeout  = "timeout"
	ResOversize = "oversize"
	ResTainted  = "tainted"
	ResPerson   = "person"
	ResNotRun   = "not-run"
)

// Criterion is one row of the locked goal as the host read it from its own rows.
type Criterion struct {
	ID       string // [a-z0-9-]{1,32}: "c1", "c2"
	Hash     string // 64 lower-case hex: the hash of the locked row
	Kind     string // command, output, file, test, rubric
	Spec     string
	Expected string
}

// Request is one check. Root is the project folder as the host resolved it from its own rows, never as the lead named it.
type Request struct {
	Root     string
	GoalHash string
	Round    int64
	Criteria []Criterion
	// JudgePin and LeadPin are read by the host at call time; the checker's own pin is the verifier's.
	JudgePin, LeadPin string
	// ExpectDigest, when set, is the tree digest the host saw while the tree was quiet; a snapshot that differs is stale.
	ExpectDigest string
	// LeadPinNow is read again after the reviewer call; if the lead's model is no longer independent of the checker
	// and the judge (or differs from LeadPin), the check is blocked (lead-changed).
	LeadPinNow func() string
	// NoLeadReread says explicitly that the lead pin is not re-read (it is not applicable). Exactly one of LeadPinNow and
	// NoLeadReread is required: a re-read that is silently absent is a bad request.
	NoLeadReread bool
}

// CriterionResult is what one criterion measured, bounded and stripped. Digests are the evidence, excerpts are for the
// person (and the reviewer, as data).
type CriterionResult struct {
	ID, Hash, Kind  string
	Result          string
	ExitCode        int
	StdoutDigest    string
	StderrDigest    string
	Stdout, Stderr  string // excerpts, <= ExcerptCap bytes, no control characters
	Detail          string
	Net             string
	RunsSubjectCode bool
}

// Result is a whole check.
type Result struct {
	Outcome Outcome
	Reason  Reason
	// GoalHash is the request's, echoed.
	GoalHash string

	SubjectDigest string
	SubjectFiles  int
	Containment   string
	Net           string // finish.NetEnforced, or "" when nothing was run

	Criteria      []CriterionResult
	ResultsDigest string

	// The reviewer (empty when it was not asked).
	ReviewerPin      string
	ReviewerObserved string
	ReviewerVerdict  string
	ReviewerReason   string

	// What the reviewer call cost, as the host charged it: micro-USD, and whether the runtime reported it (false: the
	// host charged the cap).
	CostMicro int64
	CostKnown bool
}

// Message is the plain-words line for a person, with the loud suffix when the programs ran unconfined.
func (r Result) Message() string {
	m := Message(r.Outcome, r.Reason)
	if r.Containment == ContainmentUnconfined {
		m += " (project commands ran UNCONFINED)"
	}
	return m
}

// Verifier is one goal check. A cancelled context is the outcome abandoned, not an error; a returned error is a bug, a scratch
// area that could not be made or a check already running, and the caller must not read it as an outcome.
type Verifier interface {
	Run(ctx context.Context, req Request) (Result, error)
	// Pin is the checker model's pin ("runtime/model[@version]").
	Pin() string
}
