package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// The typed operations of the strategy layer (B4). Two are callable by the planner and lead sessions of the run's tree
// (strategy.propose, strategy.report); strategy.epoch is INTERNAL: it is the host's intake of a judge result and no model
// session can ever call it. None of them can request, approve or begin a finish, raise a limit, a budget or a credential,
// or touch an approval: they only record proposals, direction progress and a judgment, and the finish decision stays with
// the finish package.
//
// Nothing here registers itself. The integrator adds the session-callable names to the allowlist (AllowedStrategy) and the
// strategy.* audit kinds to the store's reserved list; until then these handlers are libraries with no caller.

// Operation ids of the strategy layer.
const (
	OpStrategyPropose = "strategy.propose"
	OpStrategyReport  = "strategy.report"
	OpStrategyEpoch   = "strategy.epoch" // internal only: the judge result intake, never a session call
)

// Counter kinds and run-wide ceilings. They are code constants: a charter or an agent cannot raise them.
const (
	KindStrategyProposals = "strategy_proposals"
	KindStrategyReports   = "strategy_reports"

	MaxStrategyProposalsPerRun = 4 * types.MaxDirections
	MaxStrategyReportsPerRun   = 16 * types.MaxDirections

	// MaxStrategyArgsBytes bounds the raw arguments of a session call before they are decoded.
	MaxStrategyArgsBytes = 8 * 1024
)

// Errors specific to the strategy operations.
var (
	ErrNotCallable  = errors.New("doomsday ops: that operation is internal and cannot be called by a session")
	ErrStrategyCap  = errors.New("doomsday ops: the run's ceiling for this strategy operation is reached")
	ErrNotYourStrat = errors.New("doomsday ops: the direction belongs to another session")
)

// StrategyOpSpec describes one strategy operation for the allowlist registration.
type StrategyOpSpec struct {
	Name string
	// SessionCallable is true for the operations a planner or lead session may call. strategy.epoch is false: it has no
	// session entry and must never be put on a model-facing allowlist.
	SessionCallable bool
	// Counter and Cap are the cumulative per-run bound (empty for the internal operation, which is bounded by the
	// epoch's own identity and the judgment size cap).
	Counter string
	Cap     int64
}

// StrategyOpSpecs lists the operations. The result is a fresh copy.
func StrategyOpSpecs() []StrategyOpSpec {
	return []StrategyOpSpec{
		{Name: OpStrategyPropose, SessionCallable: true, Counter: KindStrategyProposals, Cap: MaxStrategyProposalsPerRun},
		{Name: OpStrategyReport, SessionCallable: true, Counter: KindStrategyReports, Cap: MaxStrategyReportsPerRun},
		{Name: OpStrategyEpoch},
	}
}

func strategySpec(op string) (StrategyOpSpec, bool) {
	for _, s := range StrategyOpSpecs() {
		if s.Name == op {
			return s, true
		}
	}
	return StrategyOpSpec{}, false
}

// AllowedStrategy says whether op is a strategy operation a session may call. The internal one is not.
func AllowedStrategy(op string) bool {
	s, ok := strategySpec(op)
	return ok && s.SessionCallable
}

// ProposeArgs are the arguments of strategy.propose. The proposer is the verified caller, never an argument.
type ProposeArgs struct {
	Title     string   `json:"title"`
	Rationale string   `json:"rationale"`
	Criteria  []string `json:"criteria"`
}

// ReportArgs are the arguments of strategy.report: a direction the caller owns moves to stalled or finished. That is the
// caller's claim about its own line of work, not a verification of any criterion.
type ReportArgs struct {
	Direction string `json:"direction"`
	State     string `json:"state"`
}

// StrategyRequest is one session call. Args is the raw JSON the model produced; it is decoded strictly here.
type StrategyRequest struct {
	Profile    string
	CharterID  string
	Hash       string
	Generation int64
	Op         string
	CallID     string // unique per call, set by the host; it makes a repeated call a replay
	Caller     Caller
	Addressed  int64
	Args       json.RawMessage
}

// StrategyResult is the typed answer to a session call.
type StrategyResult struct {
	Op          string
	Accepted    bool
	DirectionID string
	Outcome     types.NoveltyOutcome // strategy.propose
	MatchedID   string               // the row a refusal cites
	State       types.DirectionState // strategy.report
}

// StrategyProposal is a validated proposal for the sink, with the proposer taken from the verified caller.
type StrategyProposal struct {
	Profile    string
	Generation int64
	Proposal   types.Proposal
}

// ProposeOutcome is what the sink (novelty gate plus the strategy store) decided.
type ProposeOutcome struct {
	Accepted    bool
	DirectionID string
	Outcome     types.NoveltyOutcome
	MatchedID   string
}

// StrategySink is the strategy store as the ops see it. The integrator satisfies it with the novelty gate and sstore; the
// sink also enforces that the run is running (sstore does), so a stopped run is refused twice.
type StrategySink interface {
	Propose(ctx context.Context, p StrategyProposal) (ProposeOutcome, error)
	GetDirection(ctx context.Context, profile string, gen int64, id string) (types.Direction, error)
	MoveDirection(ctx context.Context, profile string, gen int64, id string, to types.DirectionState) error
	EpochExpect(ctx context.Context, profile string, gen, seq int64) (types.Expect, error)
	RecordJudgment(ctx context.Context, profile string, gen, seq int64, body []byte) (created bool, err error)
	MarkBadJudgment(ctx context.Context, profile string, gen, seq int64) error
}

// StrategyOps runs the strategy operations over an Enforcer (charter, boss tree, ledger) and a sink.
type StrategyOps struct {
	E    *Enforcer
	Sink StrategySink
}

// Handle runs one session call. It refuses, before reading anything else, an operation that is not a session-callable
// strategy operation (finish.begin, strategy.epoch, anything unknown), a run that is not running, a request that names
// another charter, generation or profile, and a caller that is not a session of the live charter's tree.
func (s *StrategyOps) Handle(ctx context.Context, r StrategyRequest) (StrategyResult, error) {
	spec, ok := strategySpec(r.Op)
	switch {
	case !ok:
		return StrategyResult{}, ErrNotAllowlisted
	case !spec.SessionCallable:
		return StrategyResult{}, fmt.Errorf("%w: %s", ErrNotCallable, r.Op)
	case s.E == nil || s.Sink == nil || s.E.Ledger == nil:
		return StrategyResult{}, fmt.Errorf("%w: strategy is not wired", ErrNotEnabled)
	}
	act, err := s.live(ctx, r.Profile, r.CharterID, r.Hash, r.Generation)
	if err != nil {
		return StrategyResult{}, err
	}
	if r.Caller.Session == 0 || r.Caller.Session != r.Addressed {
		return StrategyResult{}, ErrCallerMismatch
	}
	if err := s.E.requireBoss(ctx, act, r.Caller.Session, false); err != nil {
		return StrategyResult{}, err
	}
	if r.CallID == "" || len(r.CallID) > types.MaxIDLen {
		return StrategyResult{}, fmt.Errorf("%w: no call id", ErrBadArguments)
	}
	switch r.Op {
	case OpStrategyPropose:
		return s.propose(ctx, r, act, spec)
	default:
		return s.report(ctx, r, act, spec)
	}
}

// live is the shared entry check: a running run whose charter is the one the request names.
func (s *StrategyOps) live(ctx context.Context, profile, charterID, hash string, gen int64) (Active, error) {
	act, ok, err := s.E.Charters.Active(ctx, profile)
	if err != nil {
		return Active{}, err
	}
	if !ok {
		return Active{}, ErrNoCharter
	}
	if act.Hash != hash || act.ID != charterID || act.Generation != gen || act.Profile != profile || act.Hash == "" {
		return Active{}, ErrCharterStale
	}
	return act, nil
}

// decodeStrict reads one JSON object into v: size cap, unknown fields refused, trailing data refused.
func decodeStrict(raw json.RawMessage, v any) error {
	if len(raw) == 0 || len(raw) > MaxStrategyArgsBytes {
		return fmt.Errorf("%w: arguments are %d bytes (1 to %d)", ErrBadArguments, len(raw), MaxStrategyArgsBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", ErrBadArguments, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing data", ErrBadArguments)
	}
	return nil
}

// charge spends one unit of the operation's run-wide counter, atomically with a unique approval id (the call id), so a
// repeated call is a replay and nothing beyond the ceiling is ever admitted. An over-ceiling call is refused, not parked:
// the strategy layer raises no person item.
func (s *StrategyOps) charge(ctx context.Context, r StrategyRequest, act Active, spec StrategyOpSpec, argsHash string) error {
	id := spec.Name + ":" + r.CallID
	res, err := s.E.Ledger.Consume(ctx, Consume{
		Profile: act.Profile, CharterID: act.ID, Generation: act.Generation, ApprovalID: id,
		Spends: []Spend{{Kind: spec.Counter, N: 1, Cap: spec.Cap}},
		Audit:  AuditRow{Profile: act.Profile, CharterID: act.ID, Generation: act.Generation, Op: spec.Name, ApprovalID: id, Result: "approved", ArgsHash: argsHash},
	})
	switch {
	case err != nil:
		return err
	case res.Replay:
		return ErrAlreadyConsumed
	case !res.Done:
		return fmt.Errorf("%w: %v", ErrStrategyCap, res.Over)
	}
	return nil
}

func (s *StrategyOps) propose(ctx context.Context, r StrategyRequest, act Active, spec StrategyOpSpec) (StrategyResult, error) {
	var a ProposeArgs
	if err := decodeStrict(r.Args, &a); err != nil {
		return StrategyResult{}, err
	}
	p := types.Proposal{Title: a.Title, Rationale: a.Rationale, CriterionIDs: a.Criteria, ProposerSession: strconv.FormatInt(r.Caller.Session, 10)}
	if err := p.Validate(); err != nil {
		return StrategyResult{}, fmt.Errorf("%w: %v", ErrBadArguments, err)
	}
	h, err := ArgsHash(spec.Name, a)
	if err != nil {
		return StrategyResult{}, err
	}
	if err := s.charge(ctx, r, act, spec, h); err != nil {
		return StrategyResult{}, err
	}
	out, err := s.Sink.Propose(ctx, StrategyProposal{Profile: act.Profile, Generation: act.Generation, Proposal: p})
	if err != nil {
		return StrategyResult{}, err
	}
	return StrategyResult{Op: spec.Name, Accepted: out.Accepted, DirectionID: out.DirectionID, Outcome: out.Outcome, MatchedID: out.MatchedID}, nil
}

func (s *StrategyOps) report(ctx context.Context, r StrategyRequest, act Active, spec StrategyOpSpec) (StrategyResult, error) {
	var a ReportArgs
	if err := decodeStrict(r.Args, &a); err != nil {
		return StrategyResult{}, err
	}
	to := types.DirectionState(a.State)
	if (to != types.DirStalled && to != types.DirFinished) || a.Direction == "" || len(a.Direction) > types.MaxIDLen {
		return StrategyResult{}, fmt.Errorf("%w: a report moves a direction to stalled or finished", ErrBadArguments)
	}
	d, err := s.Sink.GetDirection(ctx, act.Profile, act.Generation, a.Direction)
	if err != nil {
		return StrategyResult{}, err
	}
	me := strconv.FormatInt(r.Caller.Session, 10)
	if d.ProposerSession != me && d.SessionID != me {
		if boss, ok, _ := s.E.Bosses.BossOf(ctx, act); !ok || boss != r.Caller.Session {
			return StrategyResult{}, ErrNotYourStrat
		}
	}
	h, err := ArgsHash(spec.Name, a)
	if err != nil {
		return StrategyResult{}, err
	}
	if err := s.charge(ctx, r, act, spec, h); err != nil {
		return StrategyResult{}, err
	}
	if err := s.Sink.MoveDirection(ctx, act.Profile, act.Generation, a.Direction, to); err != nil {
		return StrategyResult{}, err
	}
	return StrategyResult{Op: spec.Name, Accepted: true, DirectionID: a.Direction, State: to}, nil
}

// JudgeResult is the host's own delivery of a judge's output for an open epoch. It is built by the epoch engine, never
// from a session's arguments.
type JudgeResult struct {
	Profile    string
	CharterID  string
	Hash       string
	Generation int64
	Seq        int64
	Body       []byte
}

// EpochIntake is the typed answer of the intake. A judgment that fails validation is recorded as a bad-judgment epoch
// (Bad is true, Err is nil): nothing else changed, and no default verdict is invented.
type EpochIntake struct {
	Recorded bool // the judgment was stored (false for a repeat of an already stored body)
	Bad      bool
	Reason   string
}

// IntakeEpoch is strategy.epoch. It is not reachable through Handle. It refuses a run that is not running or a stale
// charter, decodes the body strictly against the criteria and directions the host expects, and stores it; the judgment
// is a record and never a finish.
func (s *StrategyOps) IntakeEpoch(ctx context.Context, j JudgeResult) (EpochIntake, error) {
	if s.E == nil || s.Sink == nil {
		return EpochIntake{}, fmt.Errorf("%w: strategy is not wired", ErrNotEnabled)
	}
	act, err := s.live(ctx, j.Profile, j.CharterID, j.Hash, j.Generation)
	if err != nil {
		return EpochIntake{}, err
	}
	if j.Seq < 1 {
		return EpochIntake{}, fmt.Errorf("%w: epoch sequence", ErrBadArguments)
	}
	exp, err := s.Sink.EpochExpect(ctx, act.Profile, act.Generation, j.Seq)
	if err != nil {
		return EpochIntake{}, err
	}
	jd, derr := types.DecodeJudgment(j.Body)
	if derr == nil {
		derr = jd.Validate(exp)
	}
	if derr != nil {
		if !errors.Is(derr, types.ErrBadJudgment) {
			return EpochIntake{}, derr
		}
		if err := s.Sink.MarkBadJudgment(ctx, act.Profile, act.Generation, j.Seq); err != nil {
			return EpochIntake{}, err
		}
		return EpochIntake{Bad: true, Reason: "judgment failed validation"}, nil
	}
	created, err := s.Sink.RecordJudgment(ctx, act.Profile, act.Generation, j.Seq, j.Body)
	if err != nil {
		return EpochIntake{}, err
	}
	return EpochIntake{Recorded: created}, nil
}
